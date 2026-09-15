package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeControlSessions struct {
	startCalls int
	stopCalls  int
	root       string
}

func (fake *fakeControlSessions) start(args []string) (sessionState, error) {
	fake.startCalls++
	state := sessionState{
		CaptureID:  "test-capture",
		Engine:     engineProxify,
		Packages:   []string{"com.example.app"},
		StartedMS:  time.Now().UnixMilli(),
		Status:     "recording",
		SessionDir: filepath.Join(fake.root, "test-capture"),
	}
	if err := writeState(state); err != nil {
		return sessionState{}, err
	}
	return state, writeSessionMetadata(state)
}

func (fake *fakeControlSessions) stop(args []string) error {
	fake.stopCalls++
	state, err := readState()
	if err != nil {
		return err
	}
	state.Status = "completed"
	state.StoppedMS = time.Now().UnixMilli()
	if err := writeSessionMetadata(state); err != nil {
		return err
	}
	return clearState()
}

func (fake *fakeControlSessions) active() (sessionState, error) {
	return readState()
}

func TestControlApplicationAuthenticatesAndRunsCaptureLifecycle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	device, token, err := createControlDevice("Pixel", engineProxify)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeControlSessions{root: t.TempDir()}
	app := newControlApplication([]byte(`{"v":4}`), "pair-token")
	app.sessions = fake
	app.ensureProxy = func(controlStartRequest, controlDevice) error { return nil }

	unauthorized := performControlRequest(app, http.MethodGet, "/control/v1/status", "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	startBody := map[string]any{
		"commandId":  "cmd-start",
		"profileId":  device.ProfileID,
		"deviceId":   device.DeviceID,
		"deviceName": "Pixel",
		"proxyHost":  "192.168.1.10",
		"proxyPort":  8888,
		"packages":   []string{"com.example.app"},
	}
	start := performControlRequest(app, http.MethodPost, "/control/v1/captures/start", token, startBody)
	if start.Code != http.StatusOK || fake.startCalls != 1 {
		t.Fatalf("start failed: code=%d calls=%d body=%s", start.Code, fake.startCalls, start.Body.String())
	}
	var started controlResponse
	if err := json.Unmarshal(start.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.CaptureID != "test-capture" || started.Status != "starting" {
		t.Fatalf("unexpected start response: %+v", started)
	}
	active, err := readState()
	if err != nil || active.Status != "starting" {
		t.Fatalf("state not marked starting: %+v err=%v", active, err)
	}
	repeatedStart := performControlRequest(app, http.MethodPost, "/control/v1/captures/start", token, startBody)
	if repeatedStart.Code != http.StatusOK || fake.startCalls != 1 {
		t.Fatalf("start idempotency failed: code=%d calls=%d", repeatedStart.Code, fake.startCalls)
	}

	confirmBody := map[string]any{"commandId": "cmd-confirm", "captureId": "test-capture"}
	confirm := performControlRequest(app, http.MethodPost, "/control/v1/captures/test-capture/vpn-started", token, confirmBody)
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm failed: %d %s", confirm.Code, confirm.Body.String())
	}
	active, err = readState()
	if err != nil || active.Status != "recording" {
		t.Fatalf("state not recording: %+v err=%v", active, err)
	}

	stopBody := map[string]any{"commandId": "cmd-stop", "captureId": "test-capture"}
	stop := performControlRequest(app, http.MethodPost, "/control/v1/captures/test-capture/stop", token, stopBody)
	if stop.Code != http.StatusOK || fake.stopCalls != 1 {
		t.Fatalf("stop failed: code=%d calls=%d body=%s", stop.Code, fake.stopCalls, stop.Body.String())
	}
	repeatedStop := performControlRequest(app, http.MethodPost, "/control/v1/captures/test-capture/stop", token, stopBody)
	if repeatedStop.Code != http.StatusOK || fake.stopCalls != 1 {
		t.Fatalf("stop idempotency failed: code=%d calls=%d", repeatedStop.Code, fake.stopCalls)
	}
}

func TestControlDevicesListAndRevoke(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	device, token, err := createControlDevice("Pixel", engineProxify)
	if err != nil {
		t.Fatal(err)
	}
	if found, ok, err := findControlDeviceByToken(token); err != nil || !ok || found.DeviceID != device.DeviceID || found.LastSeenMS == 0 {
		t.Fatalf("find did not update lastSeen: found=%+v ok=%v err=%v", found, ok, err)
	}
	var list bytes.Buffer
	if err := controlDevicesCommand(nil, &list); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.String(), device.DeviceID) || strings.Contains(list.String(), device.TokenSHA256) {
		t.Fatalf("device list missing id or leaked token digest: %s", list.String())
	}
	var revoke bytes.Buffer
	if err := controlRevokeCommand([]string{"--device-id", device.DeviceID}, &revoke); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := findControlDeviceByToken(token); err != nil || ok {
		t.Fatalf("revoked device still authenticates: ok=%v err=%v", ok, err)
	}
	var activeOnly bytes.Buffer
	if err := controlDevicesCommand(nil, &activeOnly); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(activeOnly.String(), device.DeviceID) {
		t.Fatalf("revoked device shown without --all: %s", activeOnly.String())
	}
	var all bytes.Buffer
	if err := controlDevicesCommand([]string{"--all"}, &all); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all.String(), "revoked") || !strings.Contains(all.String(), device.DeviceID) {
		t.Fatalf("revoked device not shown with --all: %s", all.String())
	}
}

func performControlRequest(app http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	var buffer bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buffer).Encode(body)
	}
	request := httptest.NewRequest(method, path, &buffer)
	request.RemoteAddr = "192.168.1.20:45678"
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if strings.TrimSpace(response.Body.String()) == "null" {
		response.Body.Reset()
	}
	return response
}

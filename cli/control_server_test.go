package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeControlSessions struct {
	startCalls   int
	stopCalls    int
	abandonCalls int
	root         string
}

func (fake *fakeControlSessions) start(args []string) (sessionState, error) {
	if active, err := readState(); err == nil && active.StoppedMS == 0 {
		return sessionState{}, errors.New("已有活动抓包会话")
	}
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

func (fake *fakeControlSessions) abandon(captureID string) (sessionState, error) {
	fake.abandonCalls++
	state, err := readState()
	if err != nil {
		return sessionState{}, err
	}
	if state.CaptureID != captureID {
		return sessionState{}, errors.New("capture mismatch")
	}
	state.Status = "abandoned"
	state.StoppedMS = time.Now().UnixMilli()
	if err := writeSessionMetadata(state); err != nil {
		return sessionState{}, err
	}
	return state, clearState()
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
	if !strings.Contains(unauthorized.Body.String(), "缺少控制凭据") {
		t.Fatalf("unauthorized body should be structured json: %s", unauthorized.Body.String())
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

func TestControlApplicationAbandonsStartingCaptureWithoutStop(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	device, token, err := createControlDevice("Pixel", engineProxify)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeControlSessions{root: t.TempDir()}
	app := newControlApplication([]byte(`{"v":4}`), "pair-token")
	app.sessions = fake
	app.ensureProxy = func(controlStartRequest, controlDevice) error { return nil }
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
	if start.Code != http.StatusOK {
		t.Fatalf("start failed: %d %s", start.Code, start.Body.String())
	}
	abandonBody := map[string]any{"commandId": "cmd-abandon", "captureId": "test-capture"}
	abandon := performControlRequest(app, http.MethodPost, "/control/v1/captures/test-capture/abandon", token, abandonBody)
	if abandon.Code != http.StatusOK || fake.abandonCalls != 1 || fake.stopCalls != 0 {
		t.Fatalf("abandon failed: code=%d abandonCalls=%d stopCalls=%d body=%s", abandon.Code, fake.abandonCalls, fake.stopCalls, abandon.Body.String())
	}
	var result controlResponse
	if err := json.Unmarshal(abandon.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "abandoned" {
		t.Fatalf("unexpected abandon response: %+v", result)
	}
	if _, err := readState(); err == nil {
		t.Fatal("active session still exists after abandon")
	}
}

func TestControlApplicationRejectsSecondStartWithoutOverwritingActiveSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	device, token, err := createControlDevice("Pixel", engineProxify)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeControlSessions{root: t.TempDir()}
	app := newControlApplication([]byte(`{"v":4}`), "pair-token")
	app.sessions = fake
	app.ensureProxy = func(controlStartRequest, controlDevice) error { return nil }
	firstBody := map[string]any{
		"commandId":  "cmd-start-one",
		"profileId":  device.ProfileID,
		"deviceId":   device.DeviceID,
		"deviceName": "Pixel",
		"proxyHost":  "192.168.1.10",
		"proxyPort":  8888,
		"packages":   []string{"com.example.app"},
	}
	first := performControlRequest(app, http.MethodPost, "/control/v1/captures/start", token, firstBody)
	if first.Code != http.StatusOK {
		t.Fatalf("first start failed: %d %s", first.Code, first.Body.String())
	}
	secondBody := map[string]any{
		"commandId":  "cmd-start-two",
		"profileId":  device.ProfileID,
		"deviceId":   device.DeviceID,
		"deviceName": "Pixel",
		"proxyHost":  "192.168.1.10",
		"proxyPort":  8888,
		"packages":   []string{"com.example.other"},
	}
	second := performControlRequest(app, http.MethodPost, "/control/v1/captures/start", token, secondBody)
	if second.Code != http.StatusConflict || fake.startCalls != 1 {
		t.Fatalf("second start should conflict without new start: code=%d calls=%d body=%s", second.Code, fake.startCalls, second.Body.String())
	}
	active, err := readState()
	if err != nil || active.CaptureID != "test-capture" || active.Status != "starting" {
		t.Fatalf("active session overwritten: state=%+v err=%v", active, err)
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
	app := newControlApplication([]byte(`{"v":4}`), "pair-token")
	revokedStatus := performControlRequest(app, http.MethodGet, "/control/v1/status", token, nil)
	if revokedStatus.Code != http.StatusUnauthorized || !strings.Contains(revokedStatus.Body.String(), "已撤销") {
		t.Fatalf("revoked token should get structured 401: code=%d body=%s", revokedStatus.Code, revokedStatus.Body.String())
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

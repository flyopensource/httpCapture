package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

const controlAPIPrefix = "/control/v1"

type controlSessionController interface {
	start(args []string) (sessionState, error)
	stop(args []string) error
	abandon(captureID string) (sessionState, error)
	active() (sessionState, error)
}

type controlPairingState struct {
	token      string
	bundle     []byte
	downloaded bool
	acked      bool
}

type controlApplication struct {
	sessions    controlSessionController
	ensureProxy func(controlStartRequest, controlDevice) error

	mu       sync.Mutex
	pairing  *controlPairingState
	commands map[string]controlCommandResult
}

type controlCommandResult struct {
	Status int
	Body   controlResponse
}

type controlResponse struct {
	OK            bool          `json:"ok"`
	Error         string        `json:"error,omitempty"`
	CaptureID     string        `json:"captureId,omitempty"`
	Status        string        `json:"status,omitempty"`
	StartTimeMS   int64         `json:"startTimeMillis,omitempty"`
	EndTimeMS     int64         `json:"endTimeMillis,omitempty"`
	SessionDir    string        `json:"sessionDir,omitempty"`
	ActiveSession *sessionState `json:"activeSession,omitempty"`
}

type controlStartRequest struct {
	CommandID  string   `json:"commandId"`
	ProfileID  string   `json:"profileId"`
	DeviceID   string   `json:"deviceId"`
	DeviceName string   `json:"deviceName"`
	ProxyHost  string   `json:"proxyHost"`
	ProxyPort  int      `json:"proxyPort"`
	Packages   []string `json:"packages"`
}

type controlCaptureRequest struct {
	CommandID string `json:"commandId"`
	CaptureID string `json:"captureId"`
}

func newControlApplication(bundle []byte, pairToken string) *controlApplication {
	return &controlApplication{
		sessions:    sessions,
		ensureProxy: ensureControlProxyReady,
		pairing: &controlPairingState{
			token: pairToken, bundle: bundle,
		},
		commands: make(map[string]controlCommandResult),
	}
}

func (app *controlApplication) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(request.URL.Path, "/p/") {
		app.servePairing(response, request)
		return
	}
	if !strings.HasPrefix(request.URL.Path, controlAPIPrefix+"/") {
		http.NotFound(response, request)
		return
	}
	device, ok := app.authenticate(response, request)
	if !ok {
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == controlAPIPrefix+"/status":
		app.handleStatus(response)
	case request.Method == http.MethodPost && request.URL.Path == controlAPIPrefix+"/captures/start":
		app.handleStart(response, request, device)
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/vpn-started"):
		app.handleConfirm(response, request, strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, controlAPIPrefix+"/captures/"), "/vpn-started"))
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/stop"):
		app.handleStop(response, request, strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, controlAPIPrefix+"/captures/"), "/stop"), device)
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/abandon"):
		app.handleAbandon(response, request, strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, controlAPIPrefix+"/captures/"), "/abandon"))
	default:
		http.NotFound(response, request)
	}
}

func (app *controlApplication) servePairing(response http.ResponseWriter, request *http.Request) {
	token := strings.TrimPrefix(request.URL.Path, "/p/")
	app.mu.Lock()
	defer app.mu.Unlock()
	if app.pairing == nil || subtle.ConstantTimeCompare([]byte(token), []byte(app.pairing.token)) != 1 {
		http.NotFound(response, request)
		return
	}
	switch request.Method {
	case http.MethodGet:
		if app.pairing.downloaded {
			http.Error(response, "pairing bundle already downloaded", http.StatusGone)
			return
		}
		app.pairing.downloaded = true
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write(app.pairing.bundle)
	case http.MethodPost:
		if !app.pairing.downloaded {
			http.Error(response, "pairing bundle was not downloaded", http.StatusConflict)
			return
		}
		app.pairing.acked = true
		response.WriteHeader(http.StatusNoContent)
	default:
		response.Header().Set("Allow", "GET, POST")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (app *controlApplication) authenticate(response http.ResponseWriter, request *http.Request) (controlDevice, bool) {
	value := request.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		writeJSON(response, http.StatusUnauthorized, controlResponse{OK: false, Error: "缺少控制凭据，请重新扫码配对"})
		return controlDevice{}, false
	}
	device, ok, err := findControlDeviceByToken(strings.TrimPrefix(value, "Bearer "))
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, controlResponse{OK: false, Error: err.Error()})
		return controlDevice{}, false
	}
	if !ok {
		writeJSON(response, http.StatusUnauthorized, controlResponse{OK: false, Error: "控制凭据无效或已撤销，请重新扫码配对"})
		return controlDevice{}, false
	}
	return device, true
}

func (app *controlApplication) handleStatus(response http.ResponseWriter) {
	state, err := app.sessions.active()
	if errors.Is(err, osErrNotExist()) {
		writeJSON(response, http.StatusOK, controlResponse{OK: true, Status: "idle"})
		return
	}
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, controlResponse{OK: false, Error: err.Error()})
		return
	}
	writeJSON(response, http.StatusOK, controlResponse{OK: true, Status: state.Status, CaptureID: state.CaptureID, ActiveSession: &state})
}

func (app *controlApplication) handleStart(response http.ResponseWriter, request *http.Request, device controlDevice) {
	var payload controlStartRequest
	if !decodeControlJSON(response, request, &payload) {
		return
	}
	if payload.CommandID == "" || payload.ProfileID != device.ProfileID || payload.DeviceID != device.DeviceID {
		writeJSON(response, http.StatusBadRequest, controlResponse{OK: false, Error: "控制请求身份或 commandId 无效"})
		return
	}
	payload.Packages = normalizePackages(payload.Packages)
	if len(payload.Packages) == 0 {
		writeJSON(response, http.StatusBadRequest, controlResponse{OK: false, Error: "至少选择一个应用"})
		return
	}
	if payload.ProxyPort < 1 || payload.ProxyPort > 65535 || strings.TrimSpace(payload.ProxyHost) == "" {
		writeJSON(response, http.StatusBadRequest, controlResponse{OK: false, Error: "代理地址无效"})
		return
	}
	if app.replyCachedCommand(response, payload.CommandID) {
		return
	}
	if app.ensureProxy != nil {
		if err := app.ensureProxy(payload, device); err != nil {
			app.rememberAndReply(response, payload.CommandID, http.StatusConflict, controlResponse{OK: false, Error: err.Error()})
			return
		}
	}
	args := []string{"--engine", device.Engine, "--device", firstNonBlank(payload.DeviceName, device.DeviceName)}
	if host, _, err := net.SplitHostPort(request.RemoteAddr); err == nil {
		args = append(args, "--client-ip", host)
	}
	if device.Engine == engineCharles {
		args = append(args, "--proxy", net.JoinHostPort(payload.ProxyHost, strconv.Itoa(payload.ProxyPort)))
	}
	for _, pkg := range payload.Packages {
		args = append(args, "--package", pkg)
	}
	state, err := app.sessions.start(args)
	if err != nil {
		app.rememberAndReply(response, payload.CommandID, http.StatusConflict, controlResponse{OK: false, Error: err.Error()})
		return
	}
	_ = markActiveSessionStatus(state.CaptureID, "starting")
	state.Status = "starting"
	app.rememberAndReply(response, payload.CommandID, http.StatusOK, controlResponse{
		OK: true, CaptureID: state.CaptureID, Status: state.Status, StartTimeMS: state.StartedMS, SessionDir: state.SessionDir,
	})
}

func (app *controlApplication) handleConfirm(response http.ResponseWriter, request *http.Request, captureID string) {
	var payload controlCaptureRequest
	if !decodeControlJSON(response, request, &payload) {
		return
	}
	if payload.CommandID == "" || payload.CaptureID != captureID || !validCaptureID(captureID) {
		writeJSON(response, http.StatusBadRequest, controlResponse{OK: false, Error: "确认请求无效"})
		return
	}
	if app.replyCachedCommand(response, payload.CommandID) {
		return
	}
	err := markActiveSessionStatus(captureID, "recording")
	if err != nil {
		app.rememberAndReply(response, payload.CommandID, http.StatusConflict, controlResponse{OK: false, Error: err.Error()})
		return
	}
	app.rememberAndReply(response, payload.CommandID, http.StatusOK, controlResponse{OK: true, CaptureID: captureID, Status: "recording"})
}

func (app *controlApplication) handleStop(response http.ResponseWriter, request *http.Request, captureID string, device controlDevice) {
	var payload controlCaptureRequest
	if !decodeControlJSON(response, request, &payload) {
		return
	}
	if payload.CommandID == "" || payload.CaptureID != captureID || !validCaptureID(captureID) {
		writeJSON(response, http.StatusBadRequest, controlResponse{OK: false, Error: "停止请求无效"})
		return
	}
	if app.replyCachedCommand(response, payload.CommandID) {
		return
	}
	active, err := app.sessions.active()
	if err != nil {
		app.rememberAndReply(response, payload.CommandID, http.StatusConflict, controlResponse{OK: false, Error: err.Error()})
		return
	}
	if active.CaptureID != captureID {
		app.rememberAndReply(response, payload.CommandID, http.StatusConflict, controlResponse{OK: false, Error: "活动会话不是请求的 captureId"})
		return
	}
	stopArgs := []string{}
	if device.Engine == engineCharles && active.ProxyHost != "" && active.ProxyPort > 0 {
		stopArgs = []string{"--proxy", net.JoinHostPort(active.ProxyHost, strconv.Itoa(active.ProxyPort))}
	}
	if err := app.sessions.stop(stopArgs); err != nil {
		app.rememberAndReply(response, payload.CommandID, http.StatusConflict, controlResponse{OK: false, Error: err.Error()})
		return
	}
	completed := controlResponse{OK: true, CaptureID: captureID, Status: "completed"}
	if state, readErr := readSessionMetadata(active.SessionDir); readErr == nil {
		completed.EndTimeMS = state.StoppedMS
		completed.SessionDir = state.SessionDir
	}
	app.rememberAndReply(response, payload.CommandID, http.StatusOK, completed)
}

func (app *controlApplication) handleAbandon(response http.ResponseWriter, request *http.Request, captureID string) {
	var payload controlCaptureRequest
	if !decodeControlJSON(response, request, &payload) {
		return
	}
	if payload.CommandID == "" || payload.CaptureID != captureID || !validCaptureID(captureID) {
		writeJSON(response, http.StatusBadRequest, controlResponse{OK: false, Error: "放弃请求无效"})
		return
	}
	if app.replyCachedCommand(response, payload.CommandID) {
		return
	}
	state, err := app.sessions.abandon(captureID)
	if err != nil {
		app.rememberAndReply(response, payload.CommandID, http.StatusConflict, controlResponse{OK: false, Error: err.Error()})
		return
	}
	app.rememberAndReply(response, payload.CommandID, http.StatusOK, controlResponse{
		OK: true, CaptureID: captureID, Status: state.Status, EndTimeMS: state.StoppedMS, SessionDir: state.SessionDir,
	})
}

func (app *controlApplication) replyCachedCommand(response http.ResponseWriter, commandID string) bool {
	app.mu.Lock()
	defer app.mu.Unlock()
	cached, ok := app.commands[commandID]
	if !ok {
		return false
	}
	writeJSON(response, cached.Status, cached.Body)
	return true
}

func (app *controlApplication) rememberAndReply(response http.ResponseWriter, commandID string, status int, body controlResponse) {
	app.mu.Lock()
	if commandID != "" {
		app.commands[commandID] = controlCommandResult{Status: status, Body: body}
	}
	app.mu.Unlock()
	writeJSON(response, status, body)
}

func decodeControlJSON(response http.ResponseWriter, request *http.Request, value any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeJSON(response, http.StatusBadRequest, controlResponse{OK: false, Error: "JSON 请求无效: " + err.Error()})
		return false
	}
	return true
}

func ensureControlProxyReady(payload controlStartRequest, device controlDevice) error {
	switch device.Engine {
	case engineProxify:
		return ensureManagedProxify(payload.ProxyPort)
	case engineCharles:
		client, err := newCharlesClient(net.JoinHostPort(payload.ProxyHost, strconv.Itoa(payload.ProxyPort)))
		if err != nil {
			return err
		}
		_, err = client.get("/")
		return err
	default:
		return fmt.Errorf("%s 暂不支持 App 联动抓包", device.Engine)
	}
}

func ensureManagedProxify(port int) error {
	if state, err := readProxifyState(); err == nil && managedProcessMatches(state.PID, state.StartToken) {
		if state.Port != port {
			return fmt.Errorf("Proxify 已运行在端口 %d，当前配置端口为 %d", state.Port, port)
		}
		return nil
	}
	return proxifyStart([]string{"--host", "0.0.0.0", "--port", strconv.Itoa(port)})
}

func listenControlAddress(host string, port int) (net.Listener, string, error) {
	listener, err := net.Listen("tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, "", fmt.Errorf("启动手机控制服务: %w", err)
	}
	url := fmt.Sprintf("https://%s/", net.JoinHostPort(host, strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)))
	return listener, url, nil
}

func serveControlListener(listener net.Listener, handler http.Handler, identity controlIdentity) *http.Server {
	server := &http.Server{
		Handler:           handler,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{identity.Certificate}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		if err := server.ServeTLS(listener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "手机控制服务中断:", err)
		}
	}()
	return server
}

func buildControlPairingBundle(engine, name, host string, port int, certPath, controlBaseURL, controlCertSHA256 string, device controlDevice, deviceToken string) ([]byte, string, error) {
	der, subject, err := readCertificate(certPath)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(name) == "" {
		name = subject
		if name == "" {
			name = net.JoinHostPort(host, strconv.Itoa(port))
		}
	}
	digest := sha256.Sum256(der)
	payload := pairing{
		Version: 4, Engine: engine, Name: name, Host: host, Port: port,
		CertificateDER:    base64.StdEncoding.EncodeToString(der),
		CertificateSHA256: strings.ToUpper(hex.EncodeToString(digest[:])),
		ControlBaseURL:    controlBaseURL,
		ControlCertSHA256: controlCertSHA256,
		ProfileID:         device.ProfileID,
		DeviceID:          device.DeviceID,
		DeviceToken:       deviceToken,
	}
	bundle, err := json.Marshal(payload)
	return bundle, payload.CertificateSHA256, err
}

func encodeControlPairingReference(downloadURL string, controlCertSHA256 string, bundle []byte) (string, error) {
	endpoint, err := url.Parse(downloadURL)
	if err != nil {
		return "", err
	}
	port := endpoint.Port()
	token := strings.TrimPrefix(endpoint.EscapedPath(), "/p/")
	if endpoint.Scheme != "https" || endpoint.Hostname() == "" || port == "" || token == endpoint.EscapedPath() || token == "" {
		return "", errors.New("控制配对地址无效")
	}
	digest := sha256.Sum256(bundle)
	return fmt.Sprintf(
		"httpcapture://p/4/%s/%s/%s/%s/%s",
		endpoint.Hostname(), port, token, strings.ToUpper(controlCertSHA256), base64.RawURLEncoding.EncodeToString(digest[:]),
	), nil
}

func printControlPairing(uri, out, terminalMode string) error {
	code, err := qrcode.New(uri, qrcode.Low)
	if err != nil {
		return fmt.Errorf("生成控制二维码: %w", err)
	}
	if out != "" {
		if err := code.WriteFile(512, out); err != nil {
			return fmt.Errorf("写入控制二维码: %w", err)
		}
	}
	return printTerminalQRCode(os.Stdout, code, terminalMode)
}

func validCaptureID(value string) bool {
	if len(value) < 10 || len(value) > 80 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func osErrNotExist() error {
	return os.ErrNotExist
}

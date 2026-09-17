package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/projectdiscovery/martian/v3"
	pdproxify "github.com/projectdiscovery/proxify"
	"github.com/projectdiscovery/proxify/pkg/certs"
	"github.com/projectdiscovery/proxify/pkg/logger/elastic"
	"github.com/projectdiscovery/proxify/pkg/logger/kafka"
	"github.com/projectdiscovery/proxify/pkg/types"
)

const (
	proxifyStateFile       = "proxify.json"
	proxifyEngineVersion   = "v0.0.16+httpcapture.1"
	proxifySourceRevision  = "cb351162a2389e4e25c702fc60c98e8e6fa988fa"
	defaultMaxBodyBytes    = 4 << 20
	maxAllowedBodyBytes    = 64 << 20
	captureSchemaVersion   = 1
	proxifyRequestStateKey = "httpcapture-request"
)

type proxifyProcessState struct {
	PID            int    `json:"pid"`
	StartToken     string `json:"startToken"`
	Executable     string `json:"executable"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	StartedMS      int64  `json:"startedMs"`
	LogPath        string `json:"logPath"`
	TrafficPath    string `json:"trafficPath"`
	ConfigDir      string `json:"configDir"`
	MaxBodyBytes   int64  `json:"maxBodyBytes"`
	Version        string `json:"version"`
	SourceRevision string `json:"sourceRevision"`
}

type capturedPayload struct {
	DeclaredSize int64  `json:"declaredSize"`
	CapturedSize int    `json:"capturedSize"`
	Encoding     string `json:"encoding,omitempty"`
	Data         string `json:"data,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	ReadError    string `json:"readError,omitempty"`
}

type capturedRequest struct {
	Method      string              `json:"method"`
	URL         string              `json:"url"`
	HTTPVersion string              `json:"httpVersion"`
	Headers     map[string][]string `json:"headers"`
	Body        capturedPayload     `json:"body"`
}

type capturedResponse struct {
	StatusCode  int                 `json:"statusCode"`
	Status      string              `json:"status"`
	HTTPVersion string              `json:"httpVersion"`
	Headers     map[string][]string `json:"headers"`
	Body        capturedPayload     `json:"body"`
}

type capturedTransaction struct {
	SchemaVersion   int              `json:"schemaVersion"`
	Timestamp       string           `json:"timestamp"`
	TimestampMillis int64            `json:"timestampMillis"`
	DurationMillis  int64            `json:"durationMillis"`
	ClientAddress   string           `json:"clientAddress,omitempty"`
	Request         capturedRequest  `json:"request"`
	Response        capturedResponse `json:"response"`
}

type proxifyRequestState struct {
	Started       time.Time
	ClientAddress string
	Request       capturedRequest
	RequestBody   *capturingReadCloser
}

type synchronizedJSONWriter struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func (w *synchronizedJSONWriter) write(value any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.encoder.Encode(value)
}

func proxifyStart(args []string) error {
	if runtime.GOOS != "linux" {
		return errors.New("当前版本仅支持在 Linux 上管理内嵌 Proxify 进程")
	}
	fs := flag.NewFlagSet("proxy proxify start", flag.ContinueOnError)
	host := fs.String("host", "0.0.0.0", "Proxify 监听地址")
	port := fs.Int("port", 8899, "Proxify 监听端口")
	maxBodyBytes := fs.Int64("max-body-bytes", defaultMaxBodyBytes, "每个请求或响应最多保存的 Body 字节数")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("proxy proxify start 不接受位置参数")
	}
	*host = strings.TrimSpace(*host)
	if err := validateListenAddress(*host, *port); err != nil {
		return err
	}
	if *maxBodyBytes < 0 || *maxBodyBytes > maxAllowedBodyBytes {
		return fmt.Errorf("--max-body-bytes 必须是 0..%d", maxAllowedBodyBytes)
	}
	if active, err := readState(); err == nil && active.Engine == engineProxify && active.StoppedMS == 0 {
		return fmt.Errorf("抓包会话 %s 仍处于记录状态；为避免切换实时数据文件，请先执行 record stop", active.CaptureID)
	}
	if previous, err := readProxifyState(); err == nil && managedProcessMatches(previous.PID, previous.StartToken) {
		return fmt.Errorf("Proxify 已由 httpcapture 启动（PID %d，%s）", previous.PID, net.JoinHostPort(previous.Host, strconv.Itoa(previous.Port)))
	}
	if err := ensurePortAvailable(*host, *port); err != nil {
		return err
	}

	root, err := proxifyRootDirectory()
	if err != nil {
		return err
	}
	runtimeDir := filepath.Join(root, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return fmt.Errorf("创建 Proxify 运行目录: %w", err)
	}
	identifier, err := randomIdentifier(6)
	if err != nil {
		return err
	}
	baseName := time.Now().Format("20060102-150405") + "-" + identifier
	trafficPath := filepath.Join(runtimeDir, baseName+".jsonl")
	logPath := filepath.Join(runtimeDir, baseName+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建 Proxify 日志: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		_ = logFile.Close()
		return fmt.Errorf("定位 httpcapture 可执行文件: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		_ = logFile.Close()
		return err
	}
	command := exec.Command(executable,
		"__proxify",
		"--host", *host,
		"--port", strconv.Itoa(*port),
		"--config-dir", root,
		"--output", trafficPath,
		"--max-body-bytes", strconv.FormatInt(*maxBodyBytes, 10),
	)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	configureManagedProcess(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("启动内嵌 Proxify: %w", err)
	}
	pid := command.Process.Pid
	startToken, err := managedProcessStartToken(pid)
	if err != nil {
		_ = terminateManagedProcess(pid)
		_ = logFile.Close()
		return fmt.Errorf("读取 Proxify 进程状态: %w", err)
	}
	if err := waitForProxifyReady(pid, startToken, *host, *port, root, 5*time.Second); err != nil {
		_ = terminateManagedProcess(pid)
		_ = logFile.Close()
		return fmt.Errorf("%w；日志: %s", err, logPath)
	}
	state := proxifyProcessState{
		PID: pid, StartToken: startToken, Executable: executable,
		Host: *host, Port: *port, StartedMS: time.Now().UnixMilli(),
		LogPath: logPath, TrafficPath: trafficPath, ConfigDir: root,
		MaxBodyBytes: *maxBodyBytes, Version: proxifyEngineVersion, SourceRevision: proxifySourceRevision,
	}
	if err := writeProxifyState(state); err != nil {
		_ = terminateManagedProcess(pid)
		_ = logFile.Close()
		return err
	}
	_ = command.Process.Release()
	_ = logFile.Close()

	fmt.Printf("Proxify 已启动，PID=%d\n监听: %s\nCA: %s\n实时数据: %s\n日志: %s\n", pid, net.JoinHostPort(*host, strconv.Itoa(*port)), filepath.Join(root, "cacert.pem"), trafficPath, logPath)
	if *host == "0.0.0.0" || *host == "::" {
		fmt.Println("配对时使用手机可访问的局域网 IP，例如: httpcapture pair --engine proxify")
	}
	return nil
}

func proxifyStop(args []string) error {
	if len(args) != 0 {
		return errors.New("proxy proxify stop 不接受参数")
	}
	state, err := readProxifyState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("没有由 httpcapture 管理的 Proxify 进程")
		}
		return err
	}
	if !managedProcessMatches(state.PID, state.StartToken) {
		return errors.New("记录的 Proxify 已不在运行；为避免误杀其他进程，未发送停止信号")
	}
	if err := terminateManagedProcess(state.PID); err != nil {
		return fmt.Errorf("停止 Proxify: %w", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for managedProcessMatches(state.PID, state.StartToken) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if managedProcessMatches(state.PID, state.StartToken) {
		return errors.New("Proxify 在 5 秒内未退出；未强制终止，请检查进程")
	}
	if err := os.Remove(proxifyStatePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Printf("Proxify 已停止，PID=%d\n实时数据保留在: %s\n", state.PID, state.TrafficPath)
	return nil
}

func proxifyStatus(args []string) error {
	if len(args) != 0 {
		return errors.New("proxy proxify status 不接受参数")
	}
	state, err := readProxifyState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("Proxify 未由 httpcapture 启动")
			return nil
		}
		return err
	}
	if !managedProcessMatches(state.PID, state.StartToken) {
		if removeErr := os.Remove(proxifyStatePath()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("清理 Proxify 过期状态: %w", removeErr)
		}
		fmt.Printf("Proxify 未运行（旧记录 PID=%d）；未操作其他进程，过期状态已清理\n", state.PID)
		return nil
	}
	fmt.Printf("Proxify 正在运行，PID=%d\n版本: %s\n源码提交: %s\n监听: %s\n启动时间(ms): %d\nCA: %s\n实时数据: %s\n日志: %s\n", state.PID, state.Version, state.SourceRevision, net.JoinHostPort(state.Host, strconv.Itoa(state.Port)), state.StartedMS, filepath.Join(state.ConfigDir, "cacert.pem"), state.TrafficPath, state.LogPath)
	return nil
}

func proxifyEngineCommand(args []string) error {
	fs := flag.NewFlagSet("__proxify", flag.ContinueOnError)
	host := fs.String("host", "", "internal")
	port := fs.Int("port", 0, "internal")
	configDir := fs.String("config-dir", "", "internal")
	outputPath := fs.String("output", "", "internal")
	maxBodyBytes := fs.Int64("max-body-bytes", defaultMaxBodyBytes, "internal")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *configDir == "" || *outputPath == "" {
		return errors.New("内嵌 Proxify 启动参数无效")
	}
	if err := validateListenAddress(*host, *port); err != nil {
		return err
	}
	if *maxBodyBytes < 0 || *maxBodyBytes > maxAllowedBodyBytes {
		return errors.New("内嵌 Proxify Body 限制无效")
	}
	if err := os.MkdirAll(*configDir, 0o700); err != nil {
		return err
	}
	if err := certs.LoadCerts(*configDir); err != nil {
		return fmt.Errorf("加载 Proxify CA: %w", err)
	}
	for _, name := range []string{"cacert.pem", "cakey.pem"} {
		if err := os.Chmod(filepath.Join(*configDir, name), 0o600); err != nil {
			return fmt.Errorf("限制 Proxify CA 文件权限: %w", err)
		}
	}
	output, err := os.OpenFile(*outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建实时抓包文件: %w", err)
	}
	defer output.Close()
	writer := &synchronizedJSONWriter{encoder: json.NewEncoder(output)}

	proxy, err := pdproxify.NewProxy(&pdproxify.Options{
		ListenAddrHTTP: *host + ":" + strconv.Itoa(*port),
		Directory:      *configDir, CertCacheSize: 256, MaxSize: int(*maxBodyBytes),
		Verbosity: types.VerbositySilent, Elastic: &elastic.Options{}, Kafka: &kafka.Options{},
		OnRequestCallback: func(req *http.Request, ctx *martian.Context) error {
			restorePlainHTTPURLScheme(req)
			state := captureRequest(req, *maxBodyBytes)
			ctx.Set(proxifyRequestStateKey, state)
			return nil
		},
		OnResponseCallback: func(resp *http.Response, ctx *martian.Context) error {
			if resp == nil || resp.Request == nil || resp.Request.Method == http.MethodConnect {
				return nil
			}
			state := proxifyRequestState{Started: time.Now(), Request: captureRequestMetadata(resp.Request)}
			if value, ok := ctx.Get(proxifyRequestStateKey); ok {
				if saved, valid := value.(proxifyRequestState); valid {
					state = saved
				}
			}
			wrapResponseCapture(state, resp, *maxBodyBytes, writer)
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("初始化 Proxify: %w", err)
	}
	fmt.Fprintf(os.Stderr, "httpCapture Proxify %s (%s) 监听 %s:%d\n", proxifyEngineVersion, proxifySourceRevision[:12], *host, *port)
	return proxy.Run()
}

func restorePlainHTTPURLScheme(req *http.Request) {
	if req == nil || req.URL == nil {
		return
	}
	// tun2proxy carries every TCP connection through CONNECT, including port 80.
	// The Martian version embedded by Proxify upgrades an origin-form request with
	// no scheme to HTTPS before this callback. A plain tunneled HTTP request has no
	// TLS state, so restore its original scheme before the upstream round trip.
	if req.TLS == nil && req.URL.Scheme == "https" {
		req.URL.Scheme = "http"
	}
}

func captureRequest(req *http.Request, maxBodyBytes int64) proxifyRequestState {
	state := proxifyRequestState{Started: time.Now()}
	if req == nil {
		return state
	}
	state.ClientAddress = req.RemoteAddr
	state.Request = captureRequestMetadata(req)
	if req.Body != nil {
		state.RequestBody = newCapturingReadCloser(req.Body, req.ContentLength, maxBodyBytes, nil)
		req.Body = state.RequestBody
	}
	return state
}

func captureRequestMetadata(req *http.Request) capturedRequest {
	if req == nil {
		return capturedRequest{}
	}
	return capturedRequest{
		Method: req.Method, URL: req.URL.String(), HTTPVersion: req.Proto,
		Headers: req.Header.Clone(), Body: emptyCapturedPayload(req.ContentLength),
	}
}

func wrapResponseCapture(state proxifyRequestState, resp *http.Response, maxBodyBytes int64, writer *synchronizedJSONWriter) {
	if state.RequestBody != nil {
		state.Request.Body = state.RequestBody.payload()
	}
	response := capturedResponse{
		StatusCode: resp.StatusCode, Status: resp.Status, HTTPVersion: resp.Proto,
		Headers: resp.Header.Clone(), Body: emptyCapturedPayload(resp.ContentLength),
	}
	complete := func(payload capturedPayload) {
		response.Body = payload
		transaction := captureResponse(state, response, time.Now())
		if err := writer.write(transaction); err != nil {
			fmt.Fprintln(os.Stderr, "写入抓包记录失败:", err)
		}
	}
	if resp.StatusCode == http.StatusSwitchingProtocols || resp.Body == nil || resp.Body == http.NoBody || resp.ContentLength == 0 {
		complete(response.Body)
		return
	}
	resp.Body = newCapturingReadCloser(resp.Body, resp.ContentLength, maxBodyBytes, complete)
}

func captureResponse(state proxifyRequestState, response capturedResponse, completed time.Time) capturedTransaction {
	duration := completed.Sub(state.Started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	return capturedTransaction{
		SchemaVersion: captureSchemaVersion,
		Timestamp:     state.Started.Format(time.RFC3339Nano), TimestampMillis: state.Started.UnixMilli(),
		DurationMillis: duration, ClientAddress: state.ClientAddress, Request: state.Request,
		Response: response,
	}
}

func emptyCapturedPayload(declaredSize int64) capturedPayload {
	return capturedPayload{DeclaredSize: declaredSize}
}

type capturingReadCloser struct {
	source       io.ReadCloser
	declaredSize int64
	maxBytes     int64
	onComplete   func(capturedPayload)

	mu         sync.Mutex
	buffer     bytes.Buffer
	totalRead  int64
	reachedEOF bool
	readError  string
	once       sync.Once
}

func newCapturingReadCloser(source io.ReadCloser, declaredSize, maxBytes int64, onComplete func(capturedPayload)) *capturingReadCloser {
	return &capturingReadCloser{
		source: source, declaredSize: declaredSize, maxBytes: maxBytes, onComplete: onComplete,
	}
}

func (r *capturingReadCloser) Read(destination []byte) (int, error) {
	count, err := r.source.Read(destination)
	r.mu.Lock()
	r.totalRead += int64(count)
	remaining := r.maxBytes - int64(r.buffer.Len())
	if remaining > 0 && count > 0 {
		captureCount := int64(count)
		if captureCount > remaining {
			captureCount = remaining
		}
		_, _ = r.buffer.Write(destination[:captureCount])
	}
	if errors.Is(err, io.EOF) {
		r.reachedEOF = true
	} else if err != nil {
		r.readError = err.Error()
	}
	r.mu.Unlock()
	if err != nil {
		r.complete()
	}
	return count, err
}

func (r *capturingReadCloser) Close() error {
	err := r.source.Close()
	if err != nil {
		r.mu.Lock()
		if r.readError == "" {
			r.readError = err.Error()
		}
		r.mu.Unlock()
	}
	r.complete()
	return err
}

func (r *capturingReadCloser) complete() {
	if r.onComplete == nil {
		return
	}
	r.once.Do(func() { r.onComplete(r.payload()) })
}

func (r *capturingReadCloser) payload() capturedPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := emptyCapturedPayload(r.declaredSize)
	setCapturedPayloadData(&result, append([]byte(nil), r.buffer.Bytes()...))
	result.ReadError = r.readError
	if r.declaredSize >= 0 {
		result.Truncated = r.declaredSize > int64(result.CapturedSize)
	} else {
		result.Truncated = !r.reachedEOF || r.totalRead > int64(result.CapturedSize)
	}
	return result
}

func setCapturedPayloadData(payload *capturedPayload, data []byte) {
	payload.CapturedSize = len(data)
	if len(data) == 0 {
		return
	}
	if utf8.Valid(data) {
		payload.Encoding = "utf8"
		payload.Data = string(data)
		return
	}
	payload.Encoding = "base64"
	payload.Data = base64.StdEncoding.EncodeToString(data)
}

func ensurePortAvailable(host string, port int) error {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("代理监听地址不可用 %s: %w", net.JoinHostPort(host, strconv.Itoa(port)), err)
	}
	return listener.Close()
}

func waitForProxifyReady(pid int, token, host string, port int, configDir string, timeout time.Duration) error {
	dialHost := host
	if host == "0.0.0.0" || host == "::" {
		dialHost = "127.0.0.1"
	}
	address := net.JoinHostPort(dialHost, strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !managedProcessMatches(pid, token) {
			return errors.New("Proxify 启动后立即退出")
		}
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			if _, err := os.Stat(filepath.Join(configDir, "cacert.pem")); err == nil {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("等待 Proxify 监听端口和 CA 超时")
}

func randomIdentifier(byteCount int) (string, error) {
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成随机标识: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func proxifyRootDirectory() (string, error) {
	directory, err := configDirectory()
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, "proxify")
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	return path, os.Chmod(path, 0o700)
}

func findProxifyCA() string {
	directory, err := proxifyRootDirectory()
	if err != nil {
		return ""
	}
	path := filepath.Join(directory, "cacert.pem")
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path
	}
	return ""
}

func proxifyStatePath() string {
	directory, err := configDirectory()
	if err != nil {
		return ""
	}
	return filepath.Join(directory, proxifyStateFile)
}

func writeProxifyState(state proxifyProcessState) error {
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	path := proxifyStatePath()
	if path == "" {
		return errors.New("无法确定 Proxify 状态文件")
	}
	return secureWrite(path, content)
}

func readProxifyState() (proxifyProcessState, error) {
	path := proxifyStatePath()
	if path == "" {
		return proxifyProcessState{}, errors.New("无法确定 Proxify 状态文件")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return proxifyProcessState{}, err
	}
	var state proxifyProcessState
	if err := json.Unmarshal(content, &state); err != nil {
		return proxifyProcessState{}, fmt.Errorf("读取 Proxify 状态: %w", err)
	}
	if state.PID <= 0 || state.StartToken == "" || state.TrafficPath == "" || state.ConfigDir == "" {
		return proxifyProcessState{}, errors.New("Proxify 状态文件无效")
	}
	return state, nil
}

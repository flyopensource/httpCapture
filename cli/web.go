package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed webui/*
var embeddedWebUI embed.FS

type webApplication struct {
	index *webIndex
	token string
	ui    fs.FS
}

func webCommand(args []string) error {
	flags := flag.NewFlagSet("web", flag.ContinueOnError)
	host := flags.String("host", "127.0.0.1", "Web 查看器监听地址（仅允许回环地址）")
	port := flags.Int("port", 9080, "Web 查看器端口；0 表示自动选择")
	sessions := flags.String("sessions", "", "抓包会话根目录")
	noOpen := flags.Bool("no-open", false, "启动后不自动打开浏览器")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("web 不接受位置参数")
	}
	if *sessions == "" {
		root, err := defaultSessionsRoot()
		if err != nil {
			return err
		}
		*sessions = root
	}
	listenHost, err := validateWebHost(*host)
	if err != nil {
		return err
	}
	if *port < 0 || *port > 65535 {
		return errors.New("--port 必须是 0..65535")
	}
	index, err := openWebIndex(*sessions)
	if err != nil {
		return fmt.Errorf("打开本地索引: %w", err)
	}
	defer index.close()
	if err := index.rescan(context.Background()); err != nil {
		return fmt.Errorf("扫描抓包会话: %w", err)
	}
	token, err := randomWebToken()
	if err != nil {
		return err
	}
	ui, err := fs.Sub(embeddedWebUI, "webui")
	if err != nil {
		return err
	}
	app := &webApplication{index: index, token: token, ui: ui}
	listener, err := net.Listen("tcp", net.JoinHostPort(listenHost, strconv.Itoa(*port)))
	if err != nil {
		return fmt.Errorf("启动 Web 查看器: %w", err)
	}
	defer listener.Close()
	actualPort := listener.Addr().(*net.TCPAddr).Port
	browserHost := listenHost
	if strings.Contains(browserHost, ":") {
		browserHost = "[" + browserHost + "]"
	}
	browserURL := fmt.Sprintf("http://%s:%d/", browserHost, actualPort)
	fmt.Println("Web 查看器:", browserURL)
	fmt.Println("会话目录:", index.root)
	fmt.Println("仅监听本机回环地址；抓包内容不会上传。")

	server := &http.Server{
		Handler:           app,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()
	if !*noOpen {
		if err := openExternal(browserURL); err != nil {
			fmt.Fprintln(os.Stderr, "提示: 无法自动打开浏览器，请手动访问上面的地址:", err)
		}
	}
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return err
		}
		return nil
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func validateWebHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "localhost") {
		return "127.0.0.1", nil
	}
	ip := net.ParseIP(value)
	if ip == nil || !ip.IsLoopback() {
		return "", errors.New("--host 仅允许 127.0.0.1、::1 或 localhost")
	}
	return value, nil
}

func randomWebToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (app *webApplication) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	app.setSecurityHeaders(response)
	if !isLoopbackRequest(request) {
		writeAPIError(response, http.StatusForbidden, "仅允许本机访问")
		return
	}
	if strings.HasPrefix(request.URL.Path, "/api/") {
		response.Header().Set("Cache-Control", "no-store")
		app.serveAPI(response, request)
		return
	}
	app.serveUI(response, request)
}

func (app *webApplication) setSecurityHeaders(response http.ResponseWriter) {
	response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
}

func isLoopbackRequest(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (app *webApplication) serveUI(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writeAPIError(response, http.StatusMethodNotAllowed, "请求方法不受支持")
		return
	}
	path := strings.TrimPrefix(request.URL.Path, "/")
	if path == "" {
		content, err := fs.ReadFile(app.ui, "index.html")
		if err != nil {
			writeAPIError(response, http.StatusInternalServerError, err.Error())
			return
		}
		content = []byte(strings.ReplaceAll(string(content), "__HTTPCAPTURE_TOKEN__", app.token))
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		_, _ = response.Write(content)
		return
	}
	if path != "app.css" && path != "app.js" {
		http.NotFound(response, request)
		return
	}
	http.FileServer(http.FS(app.ui)).ServeHTTP(response, request)
}

func (app *webApplication) serveAPI(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/api/health" && request.Method == http.MethodGet {
		writeJSON(response, http.StatusOK, map[string]any{"ok": true, "version": version})
		return
	}
	if request.URL.Path == "/api/rescan" && request.Method == http.MethodPost {
		if !app.authorizeMutation(response, request) {
			return
		}
		if err := app.index.rescan(request.Context()); err != nil {
			writeAPIError(response, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if request.URL.Path == "/api/sessions" && request.Method == http.MethodGet {
		app.handleSessions(response, request)
		return
	}
	if request.URL.Path == "/api/events" && request.Method == http.MethodGet {
		app.handleLiveEvents(response, request)
		return
	}
	if request.URL.Path == "/api/export" && request.Method == http.MethodPost {
		app.handleBatchExport(response, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/api/sessions/") {
		app.handleSessionRoute(response, request)
		return
	}
	http.NotFound(response, request)
}

func (app *webApplication) handleSessions(response http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	fromMS, err := optionalInt64(query, "fromMs")
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	toMS, err := optionalInt64(query, "toMs")
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	sessions, err := app.index.listSessions(request.Context(), query.Get("engine"), query.Get("package"), query.Get("device"), fromMS, toMS)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": sessions})
}

func (app *webApplication) handleSessionRoute(response http.ResponseWriter, request *http.Request) {
	relative := strings.TrimPrefix(request.URL.Path, "/api/sessions/")
	rawParts := strings.Split(strings.Trim(relative, "/"), "/")
	parts := make([]string, len(rawParts))
	for i, part := range rawParts {
		decoded, err := url.PathUnescape(part)
		if err != nil {
			writeAPIError(response, http.StatusBadRequest, "URL 路径无效")
			return
		}
		parts[i] = decoded
	}
	if len(parts) < 1 || validateSessionID(parts[0]) != nil {
		writeAPIError(response, http.StatusBadRequest, "会话 ID 无效")
		return
	}
	sessionID := parts[0]
	switch {
	case len(parts) == 2 && parts[1] == "requests" && request.Method == http.MethodGet:
		app.handleRequests(response, request, sessionID)
	case len(parts) == 3 && parts[1] == "requests" && request.Method == http.MethodGet:
		requestID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || requestID <= 0 {
			writeAPIError(response, http.StatusBadRequest, "请求 ID 无效")
			return
		}
		detail, err := app.index.requestDetail(request.Context(), sessionID, requestID)
		if err != nil {
			writeIndexedError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, detail)
	case len(parts) == 5 && parts[1] == "requests" && parts[3] == "body" && request.Method == http.MethodGet:
		requestID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || requestID <= 0 || (parts[4] != "request" && parts[4] != "response") {
			writeAPIError(response, http.StatusBadRequest, "Body 路径无效")
			return
		}
		app.handleBody(response, request, sessionID, requestID, parts[4])
	case len(parts) == 2 && parts[1] == "har" && request.Method == http.MethodGet:
		app.handleHAR(response, request, sessionID)
	case len(parts) == 2 && parts[1] == "open" && request.Method == http.MethodPost:
		if !app.authorizeMutation(response, request) {
			return
		}
		directory, err := app.index.sessionDirectory(request.Context(), sessionID)
		if err != nil {
			writeIndexedError(response, err)
			return
		}
		if err := openExternal(directory); err != nil {
			writeAPIError(response, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
	case len(parts) == 1 && request.Method == http.MethodDelete:
		if !app.authorizeMutation(response, request) {
			return
		}
		destination, err := app.index.moveSessionToTrash(request.Context(), sessionID)
		if err != nil {
			writeIndexedError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]string{"trashedTo": destination})
	default:
		http.NotFound(response, request)
	}
}

func (app *webApplication) handleRequests(response http.ResponseWriter, request *http.Request, sessionID string) {
	query := request.URL.Query()
	filter := requestQuery{
		SessionID:     sessionID,
		Search:        query.Get("q"),
		Method:        query.Get("method"),
		Status:        query.Get("status"),
		ContentType:   query.Get("contentType"),
		SortAscending: strings.EqualFold(query.Get("sort"), "asc"),
	}
	var err error
	if filter.FromMS, err = optionalInt64(query, "fromMs"); err == nil {
		filter.ToMS, err = optionalInt64(query, "toMs")
	}
	if err == nil {
		filter.MinDurationMS, err = optionalInt64(query, "minDurationMs")
	}
	if err == nil {
		filter.MaxDurationMS, err = optionalInt64(query, "maxDurationMs")
	}
	if err == nil {
		filter.MinSize, err = optionalInt64(query, "minSize")
	}
	if err == nil {
		filter.MaxSize, err = optionalInt64(query, "maxSize")
	}
	if err == nil {
		filter.Page, err = optionalInt(query, "page")
	}
	if err == nil {
		filter.PageSize, err = optionalInt(query, "pageSize")
	}
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	page, err := app.index.listRequests(request.Context(), filter)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(response, http.StatusOK, page)
}

func (app *webApplication) handleBody(response http.ResponseWriter, request *http.Request, sessionID string, requestID int64, side string) {
	transaction, err := app.index.requestTransaction(request.Context(), sessionID, requestID)
	if err != nil {
		writeIndexedError(response, err)
		return
	}
	payload := transaction.Request.Body
	contentType := firstHeader(transaction.Request.Headers, "Content-Type")
	if side == "response" {
		payload = transaction.Response.Body
		contentType = firstHeader(transaction.Response.Headers, "Content-Type")
	}
	content, err := payloadBytes(payload)
	if err != nil {
		writeAPIError(response, http.StatusUnprocessableEntity, err.Error())
		return
	}
	response.Header().Set("Content-Type", safeResponseContentType(contentType))
	response.Header().Set("Content-Length", strconv.Itoa(len(content)))
	if request.URL.Query().Get("download") == "1" || payload.Encoding == "base64" {
		response.Header().Set("Content-Disposition", downloadDisposition(fmt.Sprintf("%s-%d-%s.body", sessionID, requestID, side)))
	}
	_, _ = response.Write(content)
}

func safeResponseContentType(value string) string {
	mediaType, parameters, err := mime.ParseMediaType(value)
	if err != nil || mediaType == "" {
		return "application/octet-stream"
	}
	return mime.FormatMediaType(mediaType, parameters)
}

func (app *webApplication) handleHAR(response http.ResponseWriter, request *http.Request, sessionID string) {
	directory, err := app.index.sessionDirectory(request.Context(), sessionID)
	if err != nil {
		writeIndexedError(response, err)
		return
	}
	harPath := filepath.Join(directory, "session.har")
	if _, err := regularFileInfo(harPath); err != nil {
		writeIndexedError(response, err)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Content-Disposition", downloadDisposition(sessionID+".har"))
	http.ServeFile(response, request, harPath)
}

func downloadDisposition(filename string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": filename})
}

func (app *webApplication) authorizeMutation(response http.ResponseWriter, request *http.Request) bool {
	if request.Header.Get("X-HttpCapture-Token") != app.token {
		writeAPIError(response, http.StatusForbidden, "操作令牌无效，请刷新页面")
		return false
	}
	return true
}

func optionalInt64(values url.Values, key string) (int64, error) {
	value := strings.TrimSpace(values.Get(key))
	if value == "" {
		return 0, nil
	}
	result, err := strconv.ParseInt(value, 10, 64)
	if err != nil || result < 0 {
		return 0, fmt.Errorf("%s 必须是非负整数", key)
	}
	return result, nil
}

func optionalInt(values url.Values, key string) (int, error) {
	value, err := optionalInt64(values, key)
	if err != nil {
		return 0, err
	}
	if int64(int(value)) != value {
		return 0, fmt.Errorf("%s 超出范围", key)
	}
	return int(value), nil
}

func writeIndexedError(response http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		writeAPIError(response, http.StatusNotFound, "未找到请求的本地抓包数据")
		return
	}
	writeAPIError(response, http.StatusInternalServerError, err.Error())
}

func writeAPIError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]string{"error": message})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func openExternal(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		command = exec.Command("xdg-open", target)
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return fmt.Errorf("当前系统 %s 不支持自动打开", runtime.GOOS)
	}
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

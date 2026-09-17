package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWebIndexSearchFiltersAndDetail(t *testing.T) {
	root := t.TempDir()
	sessionID := "20260913-010203-proxify"
	transactions := []capturedTransaction{
		webTestTransaction(1_789_233_723_000, "POST", "https://api.example.test/v1/login?mode=fast", 201, 145, "application/json", capturedPayload{
			DeclaredSize: 18, CapturedSize: 18, Encoding: "utf8", Data: "{\"secret\":\"alpha\"}",
		}),
		webTestTransaction(1_789_233_724_000, "GET", "https://cdn.example.test/image.png", 404, 8, "image/png", capturedPayload{
			DeclaredSize: 3, CapturedSize: 3, Encoding: "base64", Data: base64.StdEncoding.EncodeToString([]byte{0, 1, 2}),
		}),
	}
	writeWebTestSession(t, root, sessionID, transactions, true)
	index, err := openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	if err := index.rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	sessions, err := index.listSessions(context.Background(), engineProxify, "example.app", "Pixel", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].IndexedRequestCount != 2 || !sessions[0].HasHAR {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	page, err := index.listRequests(context.Background(), requestQuery{
		SessionID: sessionID, Search: "secret alph", Method: "post", Status: "2xx",
		ContentType: "json", MinDurationMS: 100, MaxDurationMS: 200, MinSize: 10, Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Method != "POST" {
		t.Fatalf("unexpected filtered page: %#v", page)
	}
	detail, err := index.requestDetail(context.Background(), sessionID, page.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Query["mode"][0] != "fast" || !strings.Contains(detail.RequestBody.Data, "alpha") {
		t.Fatalf("unexpected detail: %#v", detail)
	}
	all, err := index.listRequests(context.Background(), requestQuery{SessionID: sessionID, SortAscending: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 2 || all.Items[0].TimestampMS >= all.Items[1].TimestampMS {
		t.Fatalf("ascending order failed: %#v", all.Items)
	}
	focusRules := []focusRule{{Type: "host_contains", Pattern: "api.example.test", Enabled: true}}
	focusOnly, err := index.listRequests(context.Background(), requestQuery{SessionID: sessionID, FocusMode: "only", FocusRules: focusRules})
	if err != nil {
		t.Fatal(err)
	}
	if focusOnly.Total != 1 || !focusOnly.Items[0].Focused || focusOnly.Items[0].Host != "api.example.test" {
		t.Fatalf("focus-only filter failed: %#v", focusOnly)
	}
	focusExclude, err := index.listRequests(context.Background(), requestQuery{SessionID: sessionID, FocusMode: "exclude", FocusRules: focusRules})
	if err != nil {
		t.Fatal(err)
	}
	if focusExclude.Total != 1 || focusExclude.Items[0].Focused || focusExclude.Items[0].Host != "cdn.example.test" {
		t.Fatalf("focus-exclude filter failed: %#v", focusExclude)
	}
	binaryDetail, err := index.requestDetail(context.Background(), sessionID, all.Items[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if binaryDetail.RequestBody.Data != "" || binaryDetail.RequestBody.DownloadURL == "" {
		t.Fatalf("binary body must only expose a download URL: %#v", binaryDetail.RequestBody)
	}
	if err := index.rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	again, err := index.listRequests(context.Background(), requestQuery{SessionID: sessionID})
	if err != nil || again.Total != 2 {
		t.Fatalf("rescan duplicated rows: page=%#v err=%v", again, err)
	}
}

func TestWebDetailDecodesGzipJSONForDisplay(t *testing.T) {
	root := t.TempDir()
	sessionID := "gzip-json"
	transaction := webTestTransaction(3000, "GET", "http://nweitian.paipaipeiwan.top/api/heartbeat/log", 200, 9, "application/vnd.ytapi.v1+json", capturedPayload{})
	transaction.Response.Headers["Content-Encoding"] = []string{"gzip"}
	transaction.Response.Body = gzipPayload(t, `{"code":200,"message":"ok"}`)
	writeWebTestSession(t, root, sessionID, []capturedTransaction{transaction}, false)
	index, err := openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	if err := index.rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := index.listRequests(context.Background(), requestQuery{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := index.requestDetail(context.Background(), sessionID, page.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ResponseBody.Encoding != "utf8" ||
		detail.ResponseBody.DecodedEncoding != "gzip" ||
		!strings.Contains(detail.ResponseBody.Data, `"message":"ok"`) ||
		detail.ResponseBody.DownloadURL == "" {
		t.Fatalf("gzip json was not decoded for display: %#v", detail.ResponseBody)
	}
}

func TestWebIndexSkipsMalformedLineAndMovesSessionToTrash(t *testing.T) {
	root := t.TempDir()
	sessionID := "session-safe"
	writeWebTestSession(t, root, sessionID, []capturedTransaction{
		webTestTransaction(1000, "GET", "http://example.test/ok", 200, 1, "text/plain", capturedPayload{}),
	}, false)
	trafficPath := filepath.Join(root, sessionID, "traffic.jsonl")
	file, err := os.OpenFile(trafficPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{broken\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	index, err := openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	if err := index.rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	sessions, err := index.listSessions(context.Background(), "", "", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].IndexedRequestCount != 1 || !strings.Contains(sessions[0].IndexError, "跳过 1 条") {
		t.Fatalf("unexpected malformed-line result: %#v", sessions)
	}
	destination, err := index.moveSessionToTrash(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(destination, filepath.Join(root, ".trash")+string(filepath.Separator)) {
		t.Fatalf("unsafe trash destination: %s", destination)
	}
	if _, err := os.Stat(filepath.Join(root, sessionID)); !os.IsNotExist(err) {
		t.Fatalf("session still exists: %v", err)
	}
	if info, err := os.Stat(destination); err != nil || !info.IsDir() {
		t.Fatalf("trash directory missing: %v", err)
	}
}

func TestWebApplicationLocalSecurityAndDownloads(t *testing.T) {
	root := t.TempDir()
	sessionID := "web-api"
	writeWebTestSession(t, root, sessionID, []capturedTransaction{
		webTestTransaction(2000, "POST", "https://example.test/data", 200, 5, "text/plain", capturedPayload{
			DeclaredSize: 5, CapturedSize: 5, Encoding: "utf8", Data: "hello",
		}),
	}, true)
	index, err := openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	if err := index.rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	ui, err := fs.Sub(embeddedWebUI, "webui")
	if err != nil {
		t.Fatal(err)
	}
	app := &webApplication{index: index, token: "test-token", ui: ui}
	focusPayload := `{"rules":[{"type":"url_contains","pattern":"example.test/data","enabled":true}]}`
	focusDenied := performWebRequestWithBody(app, http.MethodPut, "/api/focus", "", true, focusPayload)
	if focusDenied.Code != http.StatusForbidden {
		t.Fatalf("focus save without token: %d", focusDenied.Code)
	}
	focusSaved := performWebRequestWithBody(app, http.MethodPut, "/api/focus", "test-token", true, focusPayload)
	if focusSaved.Code != http.StatusOK {
		t.Fatalf("focus save: %d %s", focusSaved.Code, focusSaved.Body.String())
	}
	focusRead := performWebRequest(app, http.MethodGet, "/api/focus", "", true)
	if focusRead.Code != http.StatusOK || !strings.Contains(focusRead.Body.String(), "example.test/data") {
		t.Fatalf("focus read: %d %s", focusRead.Code, focusRead.Body.String())
	}
	response := performWebRequest(app, http.MethodGet, "/", "", true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "test-token") {
		t.Fatalf("index response: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing content security policy")
	}
	remote := performWebRequest(app, http.MethodGet, "/api/health", "", false)
	if remote.Code != http.StatusForbidden {
		t.Fatalf("remote request was accepted: %d", remote.Code)
	}
	list := performWebRequest(app, http.MethodGet, "/api/sessions", "", true)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), sessionID) {
		t.Fatalf("session list: %d %s", list.Code, list.Body.String())
	}
	requests := performWebRequest(app, http.MethodGet, "/api/sessions/"+sessionID+"/requests", "", true)
	var page requestPage
	if err := json.Unmarshal(requests.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("request page: %s err=%v", requests.Body.String(), err)
	}
	bodyPath := "/api/sessions/" + sessionID + "/requests/" + strconv.FormatInt(page.Items[0].ID, 10) + "/body/request"
	body := performWebRequest(app, http.MethodGet, bodyPath, "", true)
	if body.Code != http.StatusOK || body.Body.String() != "hello" {
		t.Fatalf("body response: %d %q", body.Code, body.Body.String())
	}
	har := performWebRequest(app, http.MethodGet, "/api/sessions/"+sessionID+"/har", "", true)
	if har.Code != http.StatusOK || !strings.Contains(har.Body.String(), "\"log\"") {
		t.Fatalf("HAR response: %d %s", har.Code, har.Body.String())
	}
	denied := performWebRequest(app, http.MethodDelete, "/api/sessions/"+sessionID, "", true)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("delete without token: %d", denied.Code)
	}
	deleted := performWebRequest(app, http.MethodDelete, "/api/sessions/"+sessionID, "test-token", true)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete with token: %d %s", deleted.Code, deleted.Body.String())
	}
}

func TestWebInputValidation(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "localhost"} {
		if _, err := validateWebHost(host); err != nil {
			t.Fatalf("loopback host %q rejected: %v", host, err)
		}
	}
	if _, err := validateWebHost("0.0.0.0"); err == nil {
		t.Fatal("non-loopback web host was accepted")
	}
	if err := validateSessionID("bad\r\nname"); err == nil {
		t.Fatal("unsafe session id was accepted")
	}
	if got := safeResponseContentType("text/plain; charset=utf-8"); got != "text/plain; charset=utf-8" {
		t.Fatalf("valid content type changed: %q", got)
	}
	if got := safeResponseContentType("text/plain\r\nX-Unsafe: yes"); got != "application/octet-stream" {
		t.Fatalf("unsafe content type accepted: %q", got)
	}
}

func writeWebTestSession(t *testing.T, root, id string, transactions []capturedTransaction, withHAR bool) {
	t.Helper()
	directory := filepath.Join(root, id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	state := sessionState{
		CaptureID: id, Engine: engineProxify, EngineVersion: "test", Packages: []string{"com.example.app"},
		DeviceName: "Pixel 9", StartedMS: 1_789_233_723_000, Status: "stopped", SessionDir: directory,
		RequestCount: len(transactions),
	}
	metadata, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "meta.json"), metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	var traffic bytes.Buffer
	for _, transaction := range transactions {
		line, err := json.Marshal(transaction)
		if err != nil {
			t.Fatal(err)
		}
		traffic.Write(line)
		traffic.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(directory, "traffic.jsonl"), traffic.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if withHAR {
		if err := os.WriteFile(filepath.Join(directory, "session.har"), []byte("{\"log\":{\"entries\":[]}}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func webTestTransaction(timestamp int64, method, target string, status int, duration int64, contentType string, body capturedPayload) capturedTransaction {
	return capturedTransaction{
		SchemaVersion: captureSchemaVersion, TimestampMillis: timestamp, DurationMillis: duration,
		Request: capturedRequest{
			Method: method, URL: target, HTTPVersion: "HTTP/2",
			Headers: map[string][]string{"X-Test": {"needle-header"}}, Body: body,
		},
		Response: capturedResponse{
			StatusCode: status, Status: http.StatusText(status), HTTPVersion: "HTTP/2",
			Headers: map[string][]string{"Content-Type": {contentType}},
			Body:    capturedPayload{DeclaredSize: 2, CapturedSize: 2, Encoding: "utf8", Data: "ok"},
		},
	}
}

func gzipPayload(t *testing.T, text string) capturedPayload {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return capturedPayload{
		DeclaredSize: int64(buffer.Len()), CapturedSize: buffer.Len(),
		Encoding: "base64", Data: base64.StdEncoding.EncodeToString(buffer.Bytes()),
	}
}

func performWebRequest(handler http.Handler, method, target, token string, local bool) *httptest.ResponseRecorder {
	return performWebRequestWithBody(handler, method, target, token, local, "")
}

func performWebRequestWithBody(handler http.Handler, method, target, token string, local bool, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if local {
		request.RemoteAddr = "127.0.0.1:32100"
	} else {
		request.RemoteAddr = "192.0.2.10:32100"
	}
	if token != "" {
		request.Header.Set("X-HttpCapture-Token", token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

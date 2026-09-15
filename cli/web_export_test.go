package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWebBatchExportAllFilteredAndSelected(t *testing.T) {
	root := t.TempDir()
	first := "session-first"
	second := "session-second"
	writeWebTestSession(t, root, first, []capturedTransaction{
		webTestTransaction(1000, "POST", "https://api.test/alpha", 201, 15, "application/json",
			capturedPayload{DeclaredSize: 6, CapturedSize: 6, Encoding: "utf8", Data: "secret"}),
		webTestTransaction(2000, "GET", "https://api.test/beta", 200, 12, "text/plain",
			capturedPayload{DeclaredSize: 4, CapturedSize: 4, Encoding: "utf8", Data: "body"}),
	}, false)
	writeWebTestSession(t, root, second, []capturedTransaction{
		webTestTransaction(3000, "POST", "https://api.test/gamma", 201, 13, "application/json",
			capturedPayload{DeclaredSize: 5, CapturedSize: 5, Encoding: "utf8", Data: "third"}),
	}, false)
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

	all := performWebExport(t, app, webExportSpec{Scope: "all"}, "test-token", true)
	manifest, data := unzipWebExport(t, all)
	if manifest.Status != "complete" || manifest.ExportedRecords != 3 || len(manifest.Sessions) != 2 {
		t.Fatalf("all export manifest: %+v", manifest)
	}
	if !strings.Contains(data["sessions/"+first+"/traffic.jsonl"], "secret") ||
		!strings.Contains(data["sessions/"+second+"/traffic.jsonl"], "third") {
		t.Fatalf("all export lost request bodies: %#v", data)
	}

	filtered := performWebExport(t, app, webExportSpec{
		Scope: "filtered", SessionID: first,
		RequestFilter: requestQuery{Search: "alpha", Method: "POST", Status: "2xx"},
	}, "test-token", true)
	manifest, data = unzipWebExport(t, filtered)
	if manifest.ExportedRecords != 1 || !strings.Contains(data["sessions/"+first+"/traffic.jsonl"], "secret") ||
		strings.Contains(data["sessions/"+first+"/traffic.jsonl"], "beta") {
		t.Fatalf("filtered export used wrong scope: %+v %#v", manifest, data)
	}

	page, err := index.listRequests(context.Background(), requestQuery{SessionID: first, SortAscending: true})
	if err != nil {
		t.Fatal(err)
	}
	selected := performWebExport(t, app, webExportSpec{
		Scope: "selected", SessionIDs: []string{second},
		RequestIDs: []webExportSelection{{SessionID: first, ID: page.Items[1].ID}},
	}, "test-token", true)
	manifest, data = unzipWebExport(t, selected)
	if manifest.ExportedRecords != 2 || !strings.Contains(data["sessions/"+first+"/traffic.jsonl"], "beta") ||
		strings.Contains(data["sessions/"+first+"/traffic.jsonl"], "alpha") ||
		!strings.Contains(data["sessions/"+second+"/traffic.jsonl"], "gamma") {
		t.Fatalf("selected export used wrong scope: %+v %#v", manifest, data)
	}
}

func TestWebBatchExportSecurityAndPartialManifest(t *testing.T) {
	root := t.TempDir()
	id := "session-partial"
	transaction := webTestTransaction(1000, "POST", "https://api.test/partial", 200, 4, "text/plain",
		capturedPayload{DeclaredSize: 10, CapturedSize: 4, Encoding: "utf8", Data: "part", Truncated: true})
	writeWebTestSession(t, root, id, []capturedTransaction{transaction}, false)
	trafficPath := filepath.Join(root, id, "traffic.jsonl")
	file, err := os.OpenFile(trafficPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{broken\n"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	index, err := openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	ui, err := fs.Sub(embeddedWebUI, "webui")
	if err != nil {
		t.Fatal(err)
	}
	app := &webApplication{index: index, token: "test-token", ui: ui}
	denied := performWebExport(t, app, webExportSpec{Scope: "all"}, "", true)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("export without token = %d", denied.Code)
	}
	remote := performWebExport(t, app, webExportSpec{Scope: "all"}, "test-token", false)
	if remote.Code != http.StatusForbidden {
		t.Fatalf("remote export = %d", remote.Code)
	}
	result := performWebExport(t, app, webExportSpec{Scope: "all"}, "test-token", true)
	manifest, data := unzipWebExport(t, result)
	if manifest.Status != "partial" || manifest.ExportedRecords != 1 ||
		len(manifest.Warnings) == 0 || !strings.Contains(data["sessions/"+id+"/traffic.jsonl"], "truncated") {
		t.Fatalf("partial export was misreported: %+v %#v", manifest, data)
	}
}

func TestWebBatchExportCoversAllPages(t *testing.T) {
	root := t.TempDir()
	id := "session-many-pages"
	transactions := make([]capturedTransaction, 1001)
	for index := range transactions {
		transactions[index] = webTestTransaction(
			int64(1000+index), "GET", "https://api.test/item/"+strconv.Itoa(index),
			200, 2, "text/plain", capturedPayload{},
		)
	}
	writeWebTestSession(t, root, id, transactions, false)
	index, err := openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	ui, err := fs.Sub(embeddedWebUI, "webui")
	if err != nil {
		t.Fatal(err)
	}
	app := &webApplication{index: index, token: "test-token", ui: ui}
	result := performWebExport(t, app, webExportSpec{
		Scope: "filtered", SessionID: id,
		RequestFilter: requestQuery{Search: "api.test", Status: "2xx"},
	}, "test-token", true)
	manifest, files := unzipWebExport(t, result)
	if manifest.ExportedRecords != 1001 || manifest.Status != "complete" ||
		strings.Count(files["sessions/"+id+"/traffic.jsonl"], "\n") != 1001 {
		t.Fatalf("export stopped at a page boundary: %+v", manifest)
	}
}

func TestWebBatchExportExcludesUnfinishedFinalLine(t *testing.T) {
	root := t.TempDir()
	id := "session-active-line"
	first := webTestTransaction(1000, "GET", "https://api.test/first", 200, 1, "text/plain", capturedPayload{})
	second := webTestTransaction(2000, "GET", "https://api.test/second", 200, 1, "text/plain", capturedPayload{})
	writeWebTestSession(t, root, id, []capturedTransaction{first}, false)
	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	trafficPath := filepath.Join(root, id, "traffic.jsonl")
	file, err := os.OpenFile(trafficPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(encoded); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	index, err := openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	if err := index.rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := index.listRequests(context.Background(), requestQuery{SessionID: id})
	if err != nil || page.Total != 1 {
		t.Fatalf("unfinished line was indexed: page=%+v err=%v", page, err)
	}
	ui, err := fs.Sub(embeddedWebUI, "webui")
	if err != nil {
		t.Fatal(err)
	}
	app := &webApplication{index: index, token: "test-token", ui: ui}
	exported := performWebExport(t, app, webExportSpec{Scope: "all", SessionID: id}, "test-token", true)
	manifest, files := unzipWebExport(t, exported)
	if manifest.ExportedRecords != 1 || strings.Contains(files["sessions/"+id+"/traffic.jsonl"], "second") {
		t.Fatalf("unfinished transaction was exported: %+v", manifest)
	}
	file, err = os.OpenFile(trafficPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if err := index.rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err = index.listRequests(context.Background(), requestQuery{SessionID: id})
	if err != nil || page.Total != 2 {
		t.Fatalf("completed line not indexed: page=%+v err=%v", page, err)
	}
}

func performWebExport(
	t *testing.T, app *webApplication, spec webExportSpec, token string, local bool,
) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"spec": {string(encoded)}, "token": {token}}
	request := httptest.NewRequest(http.MethodPost, "/api/export", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if local {
		request.RemoteAddr = "127.0.0.1:32100"
	} else {
		request.RemoteAddr = "192.0.2.10:32100"
	}
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	return response
}

func unzipWebExport(t *testing.T, response *httptest.ResponseRecorder) (webExportManifest, map[string]string) {
	t.Helper()
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("export response: %d %s", response.Code, response.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]string)
	var manifest webExportManifest
	for _, entry := range archive.File {
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name] = string(content)
		if entry.Name == "manifest.json" {
			if err := json.Unmarshal(content, &manifest); err != nil {
				t.Fatal(err)
			}
		}
	}
	return manifest, files
}

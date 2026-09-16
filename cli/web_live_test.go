package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func appendWebTestTraffic(t *testing.T, path string, transactions []capturedTransaction, finalNewline bool) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for index, transaction := range transactions {
		encoded, err := json.Marshal(transaction)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(encoded); err != nil {
			t.Fatal(err)
		}
		if finalNewline || index < len(transactions)-1 {
			if _, err := file.Write([]byte{'\n'}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestWebIndexReadsActiveProxifySourceThenSwitchesToArchive(t *testing.T) {
	root := t.TempDir()
	id := "session-active-proxify"
	directory := filepath.Join(root, id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "proxify-runtime.jsonl")
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := webTestTransaction(1000, "GET", "https://api.test/old", 200, 1, "text/plain", capturedPayload{})
	old.ClientAddress = "192.0.2.10:1000"
	appendWebTestTraffic(t, source, []capturedTransaction{old}, true)
	start, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	state := sessionState{
		CaptureID: id, Engine: engineProxify, EngineVersion: "test", Packages: []string{"com.example.app"},
		ClientIP: "192.0.2.10", StartedMS: 2000, Status: "recording", SessionDir: directory,
		TrafficSource: source, StartOffset: start.Size(),
	}
	if err := writeSessionMetadata(state); err != nil {
		t.Fatal(err)
	}
	first := webTestTransaction(3000, "GET", "https://api.test/first", 200, 1, "text/plain", capturedPayload{})
	first.ClientAddress = "192.0.2.10:3000"
	other := webTestTransaction(3001, "GET", "https://api.test/other-device", 200, 1, "text/plain", capturedPayload{})
	other.ClientAddress = "192.0.2.11:3001"
	appendWebTestTraffic(t, source, []capturedTransaction{first, other}, true)

	index, err := openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	index.liveSourceAllowed = func(candidate sessionState) bool { return candidate.CaptureID == id }
	check := func(want int) requestPage {
		t.Helper()
		if err := index.rescan(context.Background()); err != nil {
			t.Fatal(err)
		}
		page, err := index.listRequests(context.Background(), requestQuery{SessionID: id})
		if err != nil || page.Total != want {
			t.Fatalf("active requests=%d want=%d err=%v", page.Total, want, err)
		}
		return page
	}
	page := check(1)
	if !strings.Contains(page.Items[0].URL, "/first") {
		t.Fatalf("pre-session or other-device request leaked into active session: %+v", page.Items)
	}
	if detail, err := index.requestDetail(context.Background(), id, page.Items[0].ID); err != nil || detail.URL != first.Request.URL {
		t.Fatalf("active request detail=%+v err=%v", detail, err)
	}

	second := webTestTransaction(4000, "POST", "https://api.test/second", 201, 2, "application/json", capturedPayload{})
	second.ClientAddress = "192.0.2.10:4000"
	appendWebTestTraffic(t, source, []capturedTransaction{second}, true)
	check(2)

	end, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	trafficPath := filepath.Join(directory, "traffic.jsonl")
	count, _, err := extractCapturedTransactions(source, trafficPath, state.StartOffset, end.Size(), state.StartedMS, 5000, state.ClientIP)
	if err != nil || count != 2 {
		t.Fatalf("archive count=%d err=%v", count, err)
	}
	state.Status = "completed"
	state.StoppedMS = 5000
	state.RequestCount = count
	if err := writeSessionMetadata(state); err != nil {
		t.Fatal(err)
	}
	page = check(2)
	if detail, err := index.requestDetail(context.Background(), id, page.Items[0].ID); err != nil || detail.URL != second.Request.URL {
		t.Fatalf("archived request detail=%+v err=%v", detail, err)
	}
}

func TestWebIncrementalAppendHalfLineCorruptTruncateAndReplace(t *testing.T) {
	root := t.TempDir()
	id := "session-live"
	first := webTestTransaction(1000, "GET", "https://api.test/first", 200, 1, "text/plain", capturedPayload{})
	second := webTestTransaction(2000, "GET", "https://api.test/second", 200, 1, "text/plain", capturedPayload{})
	third := webTestTransaction(3000, "GET", "https://api.test/third", 200, 1, "text/plain", capturedPayload{})
	writeWebTestSession(t, root, id, []capturedTransaction{first}, false)
	path := filepath.Join(root, id, "traffic.jsonl")
	index, err := openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.close() }()
	check := func(count int) {
		t.Helper()
		if err := index.rescan(context.Background()); err != nil {
			t.Fatal(err)
		}
		page, err := index.listRequests(context.Background(), requestQuery{SessionID: id})
		if err != nil || page.Total != count {
			t.Fatalf("indexed total=%d want=%d err=%v", page.Total, count, err)
		}
	}
	check(1)
	firstPage, _ := index.listRequests(context.Background(), requestQuery{SessionID: id})
	firstID := firstPage.Items[0].ID
	appendWebTestTraffic(t, path, []capturedTransaction{second}, false)
	check(1)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{'\n'}); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	check(2)
	if err := index.close(); err != nil {
		t.Fatal(err)
	}
	index, err = openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	check(2)
	page, _ := index.listRequests(context.Background(), requestQuery{SessionID: id})
	if page.Items[1].ID != firstID {
		// Default sort is newest first; the original ID must remain stable.
		if page.Items[0].ID != firstID {
			t.Fatalf("incremental append renumbered original request: %d -> %+v", firstID, page.Items)
		}
	}
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{corrupt}\n"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	appendWebTestTraffic(t, path, []capturedTransaction{third}, true)
	check(3)
	cursor, err := index.loadTrafficCursor(context.Background(), id)
	if err != nil || cursor.skipped != 1 || cursor.lineNumber != 4 {
		t.Fatalf("cursor did not skip corrupt line and continue: %+v err=%v", cursor, err)
	}
	// Truncation resets the index rather than retaining stale rows.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	appendWebTestTraffic(t, path, []capturedTransaction{third}, true)
	check(1)
	// Rename + replacement changes inode and rebuilds from the new file.
	replacement := filepath.Join(root, id, "replacement.jsonl")
	originalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(first)
	if err := os.WriteFile(replacement, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, originalInfo.ModTime(), originalInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	check(1)
	page, _ = index.listRequests(context.Background(), requestQuery{SessionID: id})
	if len(page.Items) != 1 || !strings.Contains(page.Items[0].URL, "first") {
		t.Fatalf("replacement file left stale transaction: %+v", page)
	}
}

func TestWebLiveEventsStreamAndReconnect(t *testing.T) {
	root := t.TempDir()
	id := "session-events"
	first := webTestTransaction(1000, "GET", "https://api.test/first", 200, 1, "text/plain", capturedPayload{})
	writeWebTestSession(t, root, id, []capturedTransaction{first}, false)
	index, err := openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	app := &webApplication{index: index, token: "test-token"}
	server := httptest.NewServer(app)
	defer server.Close()
	client := &http.Client{Timeout: 12 * time.Second}
	response, err := client.Get(server.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events response: %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(response.Body)
	readEvent := func() (string, string) {
		t.Helper()
		var name, data string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "event: ") {
				name = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
			if line == "" && name != "" {
				return name, data
			}
		}
	}
	name, _ := readEvent()
	if name != "refresh" {
		t.Fatalf("initial event=%s", name)
	}
	second := webTestTransaction(2000, "GET", "https://api.test/second", 200, 1, "text/plain", capturedPayload{})
	appendWebTestTraffic(t, filepath.Join(root, id, "traffic.jsonl"), []capturedTransaction{second}, true)
	for {
		name, data := readEvent()
		if name != "requests" {
			continue
		}
		var items []liveRequestSummary
		if err := json.Unmarshal([]byte(data), &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].SessionID != id || !strings.Contains(items[0].URL, "second") {
			t.Fatalf("bad live summary: %+v", items)
		}
		break
	}
	_ = response.Body.Close()
	// A new stream starts with a full refresh, so no Last-Event-ID is needed
	// to recover changes made while the browser was disconnected.
	third := webTestTransaction(3000, "GET", "https://api.test/third", 200, 1, "text/plain", capturedPayload{})
	appendWebTestTraffic(t, filepath.Join(root, id, "traffic.jsonl"), []capturedTransaction{third}, true)
	response, err = client.Get(server.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	reader = bufio.NewReader(response.Body)
	name, _ = readEvent()
	if name != "refresh" {
		t.Fatalf("reconnect event=%s", name)
	}
	_ = response.Body.Close()
	if err := index.rescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := index.listRequests(context.Background(), requestQuery{SessionID: id})
	if err != nil || page.Total != 3 {
		t.Fatalf("reconnect lost data: %+v err=%v", page, err)
	}
}

func TestWebLiveEventsThousandRequestsNoDuplicate(t *testing.T) {
	root := t.TempDir()
	id := "session-thousand-live"
	first := webTestTransaction(1000, "GET", "https://api.test/first", 200, 1, "text/plain", capturedPayload{})
	writeWebTestSession(t, root, id, []capturedTransaction{first}, false)
	index, err := openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	server := httptest.NewServer(&webApplication{index: index, token: "test-token"})
	defer server.Close()
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Get(server.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	// Consume the initial reconnect/full-refresh event.
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			break
		}
	}
	transactions := make([]capturedTransaction, 1000)
	for n := range transactions {
		transactions[n] = webTestTransaction(int64(2000+n), "GET", "https://api.test/item/"+strconv.Itoa(n),
			200, 1, "text/plain", capturedPayload{})
	}
	appendWebTestTraffic(t, filepath.Join(root, id, "traffic.jsonl"), transactions, true)
	seen := make(map[int64]bool)
	var name, data string
	for len(seen) < 1000 {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("live stream stopped after %d requests: %v", len(seen), err)
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event: ") {
			name = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
		if line != "" || name == "" {
			continue
		}
		if name == "requests" {
			var items []liveRequestSummary
			if err := json.Unmarshal([]byte(data), &items); err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if seen[item.RequestID] {
					t.Fatalf("duplicate live request %d", item.RequestID)
				}
				seen[item.RequestID] = true
			}
		}
		name, data = "", ""
	}
	if len(seen) != 1000 {
		t.Fatalf("got %d live requests", len(seen))
	}
}

func TestWebLiveEventsResetOnReplacement(t *testing.T) {
	root := t.TempDir()
	id := "session-reset-event"
	first := webTestTransaction(1000, "GET", "https://api.test/first", 200, 1, "text/plain", capturedPayload{})
	writeWebTestSession(t, root, id, []capturedTransaction{first}, false)
	index, err := openWebIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	defer index.close()
	server := httptest.NewServer(&webApplication{index: index})
	defer server.Close()
	client := &http.Client{Timeout: 8 * time.Second}
	response, err := client.Get(server.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			break
		}
	}
	path := filepath.Join(root, id, "traffic.jsonl")
	previous, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, id, "new.jsonl")
	other := webTestTransaction(1000, "GET", "https://api.test/other", 200, 1, "text/plain", capturedPayload{})
	encoded, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, previous.ModTime(), previous.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	var name, data string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("replacement reset event missing: %v", err)
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event: ") {
			name = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
		if line != "" || name == "" {
			continue
		}
		if name == "reset" {
			var sessions []string
			if err := json.Unmarshal([]byte(data), &sessions); err != nil {
				t.Fatal(err)
			}
			if len(sessions) != 1 || sessions[0] != id {
				t.Fatalf("wrong reset: %v", sessions)
			}
			break
		}
		name, data = "", ""
	}
}

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

func TestEncodePairingReferenceRoundTrip(t *testing.T) {
	bundle := []byte(`{"v":3,"engine":"charles","name":"dev"}`)
	uri, err := encodePairingReference("http://192.168.1.10:43210/p/token", bundle)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "httpcapture://p/3/192.168.1.10/43210/token/"
	if !strings.HasPrefix(uri, prefix) {
		t.Fatalf("unexpected URI: %s", uri)
	}
	digest, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != sha256.Size {
		t.Fatalf("digest length = %d", len(digest))
	}
}

func TestPairingServerDownloadsOnceAndWaitsForAck(t *testing.T) {
	bundle := []byte(`{"v":3,"engine":"charles","name":"dev"}`)
	server, err := startPairingServer("127.0.0.1", 0, bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	response, err := http.Get(server.URL())
	if err != nil {
		t.Fatal(err)
	}
	content, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || string(content) != string(bundle) {
		t.Fatalf("unexpected download: status=%d body=%q err=%v", response.StatusCode, content, readErr)
	}
	second, err := http.Get(server.URL())
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Body.Close()
	if second.StatusCode != http.StatusGone {
		t.Fatalf("second download status = %d", second.StatusCode)
	}

	request, err := http.NewRequest(http.MethodPost, server.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = ack.Body.Close()
	if ack.StatusCode != http.StatusNoContent {
		t.Fatalf("ack status = %d", ack.StatusCode)
	}
	if err := server.Wait(time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPairingServerRejectsAckBeforeDownload(t *testing.T) {
	server, err := startPairingServer("127.0.0.1", 0, []byte(`{"v":3}`))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL(), nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestCompactTerminalQRCodeDimensions(t *testing.T) {
	code, err := qrcode.New("httpcapture://p/3/127.0.0.1/39001/token/digest", qrcode.Low)
	if err != nil {
		t.Fatal(err)
	}
	text, columns, rows := compactTerminalQRCode(code)
	bitmap := code.Bitmap()
	if columns != len(bitmap[0]) {
		t.Fatalf("columns = %d, want %d", columns, len(bitmap[0]))
	}
	if rows != (len(bitmap)+1)/2 {
		t.Fatalf("rows = %d, want %d", rows, (len(bitmap)+1)/2)
	}
	if !strings.ContainsAny(text, "▀▄█") {
		t.Fatal("compact QR did not use half-block characters")
	}
}

func TestV3PairingQRCodeFitsCommonTerminal(t *testing.T) {
	bundle := []byte(`{"v":3,"engine":"charles","name":"Charles Proxy CA","certificateDer":"AQID"}`)
	uri, err := encodePairingReference("http://192.168.123.123:54321/p/1234567890123456789012", bundle)
	if err != nil {
		t.Fatal(err)
	}
	code, err := qrcode.New(uri, qrcode.Low)
	if err != nil {
		t.Fatal(err)
	}
	_, columns, rows := compactTerminalQRCode(code)
	if columns >= 80 || rows >= 24 {
		t.Fatalf("QR requires %dx%d terminal cells", columns, rows)
	}
}

func TestTerminalQRCodeAutoSizing(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		interactive bool
		columns     int
		rows        int
		want        bool
	}{
		{name: "auto fits", mode: terminalQRAuto, interactive: true, columns: 120, rows: 60, want: true},
		{name: "auto too narrow", mode: terminalQRAuto, interactive: true, columns: 79, rows: 60, want: false},
		{name: "auto too short", mode: terminalQRAuto, interactive: true, columns: 120, rows: 39, want: false},
		{name: "auto exact boundary", mode: terminalQRAuto, interactive: true, columns: 80, rows: 40, want: false},
		{name: "auto redirected", mode: terminalQRAuto, interactive: false, columns: 120, rows: 60, want: false},
		{name: "always redirected", mode: terminalQRAlways, interactive: false, want: true},
		{name: "never", mode: terminalQRNever, interactive: true, columns: 120, rows: 60, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := shouldPrintTerminalQRCode(test.mode, test.interactive, test.columns, test.rows, 80, 40)
			if got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidateTerminalQRMode(t *testing.T) {
	for _, mode := range []string{terminalQRAuto, terminalQRAlways, terminalQRNever} {
		if err := validateTerminalQRMode(mode); err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
	if err := validateTerminalQRMode("small"); err == nil {
		t.Fatal("expected invalid mode error")
	}
}

func TestReadCertificateRejectsPrivateBundleAndAcceptsCA(t *testing.T) {
	path, der := writeTestCA(t)
	got, subject, err := readCertificate(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(der) || subject != "HTTP Capture Test CA" {
		t.Fatalf("unexpected cert result: %q", subject)
	}
}

func writeTestCA(t *testing.T) (string, []byte) {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "HTTP Capture Test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.cer")
	if err := os.WriteFile(path, der, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, der
}

func TestProxyEngineDefaultsAndMitmproxyCA(t *testing.T) {
	if defaultProxyPort(engineCharles) != 8888 || defaultProxyPort(engineCustom) != 8888 {
		t.Fatal("Charles/custom default port changed")
	}
	if defaultProxyPort(engineMitmproxy) != 8080 {
		t.Fatal("mitmproxy default port must be 8080")
	}
	if validProxyEngine("MITMPROXY") || validProxyEngine("unknown") {
		t.Fatal("engine validation must only accept normalized supported values")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	caPath := filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.cer")
	if err := os.MkdirAll(filepath.Dir(caPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caPath, []byte("ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := findMitmproxyCA(); got != caPath {
		t.Fatalf("findMitmproxyCA() = %q, want %q", got, caPath)
	}
}

func TestPairRejectsExplicitZeroPort(t *testing.T) {
	err := pairCommand([]string{"--engine", "mitmproxy", "--host", "127.0.0.1", "--port", "0"})
	if err == nil || !strings.Contains(err.Error(), "1..65535") {
		t.Fatalf("expected explicit zero port rejection, got %v", err)
	}
}

func TestManagedMitmdumpLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only managed process test")
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	caPath := filepath.Join(root, "home", ".mitmproxy", "mitmproxy-ca-cert.cer")
	if err := os.MkdirAll(filepath.Dir(caPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caPath, []byte("test-ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "mitmdump")
	script := "#!/bin/sh\ntrap 'exit 0' TERM INT\nwhile true; do sleep 1; done\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := mitmStart([]string{"--bin", binary, "--host", "127.0.0.1", "--port", "18080"}); err != nil {
		t.Fatal(err)
	}
	state, err := readMitmState()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if managedProcessMatches(state.PID, state.StartToken) {
			_ = terminateManagedProcess(state.PID)
		}
	})
	if !managedProcessMatches(state.PID, state.StartToken) {
		t.Fatal("managed mitmdump is not running")
	}
	info, err := os.Stat(mitmStatePath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o", info.Mode().Perm())
	}
	if err := mitmStatus(nil); err != nil {
		t.Fatal(err)
	}
	if err := mitmStop(nil); err != nil {
		t.Fatal(err)
	}
	if managedProcessMatches(state.PID, state.StartToken) {
		t.Fatal("managed mitmdump is still running")
	}
	if _, err := os.Stat(mitmStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file still exists: %v", err)
	}
}

func TestMitmproxyPairingV3EndToEnd(t *testing.T) {
	certPath, _ := writeTestCA(t)
	root := t.TempDir()
	uriPath := filepath.Join(root, "pair.txt")
	result := make(chan error, 1)
	go func() {
		result <- pairCommand([]string{
			"--engine", "mitmproxy",
			"--host", "127.0.0.1",
			"--cert", certPath,
			"--name", "test-mitm",
			"--out", filepath.Join(root, "pair.png"),
			"--uri-out", uriPath,
			"--terminal-qr", "never",
			"--timeout", "3s",
		})
	}()

	var rawURI []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rawURI, _ = os.ReadFile(uriPath)
		if len(rawURI) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	endpoint, err := url.Parse(strings.TrimSpace(string(rawURI)))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.Trim(endpoint.Path, "/"), "/")
	if endpoint.Scheme != "httpcapture" || endpoint.Host != "p" || len(parts) != 5 || parts[0] != "3" {
		t.Fatalf("unexpected pairing URI: %q", rawURI)
	}
	downloadURL := "http://" + parts[1] + ":" + parts[2] + "/p/" + parts[3]
	response, err := http.Get(downloadURL)
	if err != nil {
		t.Fatal(err)
	}
	var payload pairing
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if payload.Version != 3 || payload.Engine != engineMitmproxy || payload.Port != 8080 {
		t.Fatalf("unexpected pairing payload: %+v", payload)
	}
	request, _ := http.NewRequest(http.MethodPost, downloadURL, nil)
	ack, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = ack.Body.Close()
	if ack.StatusCode != http.StatusNoContent {
		t.Fatalf("ack status = %d", ack.StatusCode)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pair command did not exit after acknowledgement")
	}
}

func TestFilterXMLByTimeAndClient(t *testing.T) {
	input := filepath.Join(t.TempDir(), "session.xml")
	output := filepath.Join(t.TempDir(), "filtered.xml")
	document := `<?xml version="1.0"?><charles-session><transaction startTimeMillis="100" clientAddress="192.168.1.9:123"><host>a</host></transaction><transaction startTimeMillis="200" clientAddress="192.168.1.10:234"><host>b</host></transaction></charles-session>`
	if err := os.WriteFile(input, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := filterXML(input, output, exportFilter{fromMS: 150, toMS: 250, clientIP: "192.168.1.10"}); err != nil {
		t.Fatal(err)
	}
	result, _ := os.ReadFile(output)
	if strings.Contains(string(result), "<host>a</host>") || !strings.Contains(string(result), "<host>b</host>") {
		t.Fatalf("unexpected filtered XML: %s", result)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(result)))
	for {
		if _, err := decoder.Token(); err != nil {
			if err.Error() != "EOF" {
				t.Fatalf("invalid output XML: %v\n%s", err, result)
			}
			break
		}
	}
}

func TestCharlesDisabledDetection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("Charles Web Interface is disabled"))
	}))
	defer server.Close()
	client, err := newCharlesClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.get("/"); err == nil || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("expected disabled error, got %v", err)
	}
}

func TestCharlesAuthenticationUsesEnvironmentWithoutLoggingSecret(t *testing.T) {
	t.Setenv("HTTPCAPTURE_CHARLES_USERNAME", "dev")
	t.Setenv("HTTPCAPTURE_CHARLES_PASSWORD", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "dev" || password != "secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()
	client, err := newCharlesClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.get("/"); err != nil {
		t.Fatal(err)
	}
}

func TestRecordStartStopAndSafeClearWorkflow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.Path)
		switch request.URL.Path {
		case "/session/download":
			_, _ = writer.Write([]byte("chls-data"))
		case "/session/export-xml":
			_, _ = writer.Write([]byte("<charles-session/>"))
		case "/session/export-har":
			_, _ = writer.Write([]byte(`{"log":{"entries":[]}}`))
		default:
			_, _ = writer.Write([]byte("ok"))
		}
	}))
	defer server.Close()

	if err := recordStart([]string{"--proxy", server.URL, "--clear", "--app", "com.example.one", "--client-ip", "192.168.1.9"}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "output")
	if err := recordStop([]string{"--proxy", server.URL, "--out", output, "--formats", "chls,xml,har"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/session/download", "/session/clear", "/recording/start", "/recording/stop",
		"/session/download", "/session/export-xml", "/session/export-har",
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("unexpected Charles request order:\n got %v\nwant %v", requests, want)
	}
	for _, name := range []string{"session.chls", "session.xml", "session.har", "capture.json"} {
		info, err := os.Stat(filepath.Join(output, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o", name, info.Mode().Perm())
		}
	}
	backups, err := filepath.Glob(filepath.Join(root, "home", "httpcapture-sessions", "*-pre-clear", "backup.chls"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one pre-clear backup, got %v, err=%v", backups, err)
	}
}

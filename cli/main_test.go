package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

func TestEncodePairingRoundTrip(t *testing.T) {
	input := pairing{Version: 1, Name: "dev", Host: "10.0.2.2", Port: 8888, CertificateDER: "AQID", CertificateSHA256: strings.Repeat("A", 64)}
	uri, err := encodePairing(input)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "httpcapture://pair/v1/"
	if !strings.HasPrefix(uri, prefix) {
		t.Fatalf("unexpected URI: %s", uri)
	}
	compressed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
	if err != nil {
		t.Fatal(err)
	}
	decoded := gunzipForTest(t, compressed)
	var output pairing
	if err := json.Unmarshal(decoded, &output); err != nil {
		t.Fatal(err)
	}
	if output != input {
		t.Fatalf("round trip mismatch: %#v", output)
	}
}

func TestCompactTerminalQRCodeDimensions(t *testing.T) {
	code, err := qrcode.New("httpcapture://pair/v1/test", qrcode.Medium)
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
	got, subject, err := readCertificate(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(der) || subject != "HTTP Capture Test CA" {
		t.Fatalf("unexpected cert result: %q", subject)
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

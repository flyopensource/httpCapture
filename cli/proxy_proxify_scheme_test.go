package main

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestRestorePlainHTTPURLScheme(t *testing.T) {
	tests := []struct {
		name       string
		rawURL     string
		tlsState   *tls.ConnectionState
		wantScheme string
	}{
		{name: "tunneled plain HTTP", rawURL: "https://example.test/path", wantScheme: "http"},
		{name: "MITM HTTPS", rawURL: "https://example.test/path", tlsState: &tls.ConnectionState{}, wantScheme: "https"},
		{name: "regular HTTP", rawURL: "http://example.test/path", wantScheme: "http"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, test.rawURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.TLS = test.tlsState
			restorePlainHTTPURLScheme(req)
			if req.URL.Scheme != test.wantScheme {
				t.Fatalf("scheme = %q, want %q", req.URL.Scheme, test.wantScheme)
			}
		})
	}

	restorePlainHTTPURLScheme(nil)
}

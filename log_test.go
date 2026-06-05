package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(routes())
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("GET /healthz -> %d, want 204", resp.StatusCode)
	}
}

func TestAccessLogSkipsHealthz(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	h := accessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil))
	if buf.Len() != 0 {
		t.Errorf("/healthz should not be logged, got: %q", buf.String())
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/jitter/inspect", nil))
	if buf.Len() == 0 {
		t.Error("non-healthz request should be logged")
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		xRealIP    string
		want       string
	}{
		{"peer only", "10.244.1.34:57496", "", "", "10.244.1.34"},
		{"x-forwarded-for single", "10.0.0.1:80", "203.0.113.7", "", "203.0.113.7"},
		{"x-forwarded-for chain", "10.0.0.1:80", "203.0.113.7, 10.244.2.1, 10.244.1.1", "", "203.0.113.7"},
		{"x-forwarded-for spaces", "10.0.0.1:80", "  198.51.100.9 , 10.0.0.2 ", "", "198.51.100.9"},
		{"x-real-ip fallback", "10.0.0.1:80", "", "198.51.100.23", "198.51.100.23"},
		{"xff wins over x-real-ip", "10.0.0.1:80", "203.0.113.7", "198.51.100.23", "203.0.113.7"},
		{"ipv6 peer", "[2a01:4f8::1]:443", "", "", "2a01:4f8::1"},
		{"empty xff ignored", "10.244.1.34:57496", "  ", "", "10.244.1.34"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remoteAddr
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if c.xRealIP != "" {
				r.Header.Set("X-Real-IP", c.xRealIP)
			}
			if got := clientIP(r); got != c.want {
				t.Errorf("clientIP() = %q, want %q", got, c.want)
			}
		})
	}
}

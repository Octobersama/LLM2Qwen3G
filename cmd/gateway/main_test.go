package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestRunHealthcheck(t *testing.T) {
	client := &http.Client{Timeout: time.Second}

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	if code := probeHealthz(hostPort(t, ok.URL), client); code != 0 {
		t.Fatalf("healthy server: exit %d, want 0", code)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	if code := probeHealthz(hostPort(t, bad.URL), client); code != 1 {
		t.Fatalf("non-200 server: exit %d, want 1", code)
	}

	if code := probeHealthz("127.0.0.1:1", client); code != 1 { // nothing listening
		t.Fatalf("no server: exit %d, want 1", code)
	}
}

func TestHealthzURL(t *testing.T) {
	cases := map[string]string{
		"":             "http://127.0.0.1:8080/healthz",
		":9000":        "http://127.0.0.1:9000/healthz",
		"127.0.0.1:80": "http://127.0.0.1:80/healthz",
		"[::1]:8080":   "http://[::1]:8080/healthz",
	}
	for addr, want := range cases {
		got, err := healthzURL(addr)
		if err != nil || got != want {
			t.Errorf("healthzURL(%q) = %q, %v; want %q", addr, got, err, want)
		}
	}
	if _, err := healthzURL("no-port"); err == nil {
		t.Error("healthzURL(no-port) must error")
	}
}

// hostPort extracts the host:port from an httptest server URL via net/url.
func hostPort(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Host
}

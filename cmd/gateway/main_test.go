package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunHealthcheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	t.Setenv("LISTEN_ADDR", "127.0.0.1"+portOf(ok.URL))
	if code := runHealthcheck(); code != 0 {
		t.Fatalf("healthy server: exit %d, want 0", code)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	t.Setenv("LISTEN_ADDR", "127.0.0.1"+portOf(bad.URL))
	if code := runHealthcheck(); code != 1 {
		t.Fatalf("non-200 server: exit %d, want 1", code)
	}

	t.Setenv("LISTEN_ADDR", "127.0.0.1:1") // nothing listening
	if code := runHealthcheck(); code != 1 {
		t.Fatalf("no server: exit %d, want 1", code)
	}
}

func portOf(url string) string {
	for i := len(url) - 1; i >= 0; i-- {
		if url[i] == ':' {
			return url[i:]
		}
	}
	return url
}

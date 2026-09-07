package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"llm2qwen3guard/internal/config"
	"llm2qwen3guard/internal/logsys"
	"llm2qwen3guard/internal/upstream"
)

// testLogger returns a logger whose stdout sink is io.Discard: level
// filtering alone is not enough because failure-policy tests emit
// ERROR-level audit_failed events, which would still print. File sink is
// off ("").
func testLogger(t *testing.T) *logsys.Logger {
	t.Helper()
	l := logsys.NewDiscard()
	return l
}

func testConfig(base string) config.Config {
	return config.Config{UpstreamBaseURL: base, UpstreamAPIKey: "k", UpstreamModel: "m", UpstreamTimeout: 1, UpstreamMaxTokens: 64, StructuredOutputMode: "json_object", MaxInputChars: 4, FailurePolicy: "error"}
}
func TestServerEndToEnd(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"safety\":\"Unsafe\",\"categories\":[\"violence\"]}"}}]}`))
	}))
	defer up.Close()
	h := NewWithClient(testConfig(up.URL), &upstream.Client{BaseURL: up.URL, APIKey: "k", Model: "m", Timeout: time.Second, StructuredOutputMode: "json_object"}, testLogger(t))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"ignored","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var env map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &env) != nil {
		t.Fatal("invalid envelope")
	}
	choice := env["choices"].([]any)[0].(map[string]any)
	got := choice["message"].(map[string]any)["content"]
	if got != "Safety: Unsafe\nCategories: Violent" {
		t.Fatalf("content=%v", got)
	}
}
func TestServerStreamAndAuthAndEmpty(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"safety\":\"Safe\",\"categories\":[]}"}}]}`))
	}))
	defer up.Close()
	cfg := testConfig(up.URL)
	cfg.MaxInputChars = 0
	cfg.GatewayAPIKey = "secret"
	h := NewWithClient(cfg, &upstream.Client{BaseURL: up.URL, APIKey: "k", Model: "m", Timeout: time.Second, StructuredOutputMode: "json_object"}, testLogger(t))
	bad := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	bad.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, bad)
	if rr.Code != 400 {
		t.Fatalf("empty status %d", rr.Code)
	}
	unauth := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"x"}]}`))
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, unauth)
	if rr.Code != 401 {
		t.Fatalf("auth status %d", rr.Code)
	}
	stream := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"x"}]}`))
	stream.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, stream)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "data: [DONE]") || strings.Count(rr.Body.String(), "data:") != 4 || !strings.Contains(rr.Body.String(), `"finish_reason":"stop"`) {
		t.Fatalf("stream=%q", rr.Body.String())
	}
}
func TestFailurePoliciesAndTruncation(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		ms := b["messages"].([]any)
		user := ms[1].(map[string]any)
		if user["content"] != "1234" {
			t.Errorf("content=%v", user["content"])
		}
		w.WriteHeader(500)
	}))
	defer up.Close()
	for _, policy := range []string{"error", "safe", "unsafe"} {
		cfg := testConfig(up.URL)
		cfg.FailurePolicy = policy
		h := NewWithClient(cfg, &upstream.Client{BaseURL: up.URL, APIKey: "k", Model: "m", Timeout: time.Second, StructuredOutputMode: "json_object"}, testLogger(t))
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"123456"}]}`))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		want := 200
		if policy == "error" {
			want = 503
		}
		if rr.Code != want {
			t.Fatalf("%s status=%d", policy, rr.Code)
		}
		if policy != "error" && !strings.Contains(rr.Body.String(), "Categories: None") {
			t.Fatal(rr.Body.String())
		}
	}
}

func TestRequestBodyLimit(t *testing.T) {
	// Inject a tiny limit instead of allocating a >1MiB body: oversize is
	// oversize regardless of the threshold.
	cfg := testConfig("http://unused.example")
	cfg.MaxRequestBytes = 64
	h := NewWithClient(cfg, &upstream.Client{BaseURL: "http://unused.example", APIKey: "k", Model: "m", Timeout: time.Second, StructuredOutputMode: "json_object"}, testLogger(t))
	big := `{"messages":[{"role":"user","content":"` + strings.Repeat("x", 256) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(big))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

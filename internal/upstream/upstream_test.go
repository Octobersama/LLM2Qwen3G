package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientNegotiation(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"safety\":\"Safe\",\"categories\":[]}"}}],"usage":{"total_tokens":3}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/v1/", APIKey: "key", Model: "m", Timeout: time.Second, MaxTokens: 64, StructuredOutputMode: "auto"}
	content, usage, modeUsed, err := c.Do(context.Background(), AuditRequest{Text: "hello"})
	if err != nil || !strings.Contains(content, "Safe") || len(usage) == 0 || modeUsed != "json_object" {
		t.Fatalf("content=%q usage=%s mode=%s err=%v", content, usage, modeUsed, err)
	}
	if bodies[0]["response_format"].(map[string]any)["type"] != "json_schema" || bodies[1]["response_format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("bad modes %#v", bodies)
	}
	// strict defaults to omitted (SiliconFlow does not document it under
	// response_format; docs.siliconflow.com/cn/userguide/guides/json-mode_struct).
	if _, ok := bodies[0]["response_format"].(map[string]any)["json_schema"].(map[string]any)["strict"]; ok {
		t.Fatal("strict must be omitted by default")
	}
	// The degraded retry must reuse the same prompt system policy.
	if bodies[0]["messages"] == nil || bodies[1]["messages"] == nil {
		t.Fatal("missing messages")
	}
}

func TestClientJsonSchemaStrictOptIn(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"safety\":\"Unsafe\",\"categories\":[\"Violent\"]}"}}]}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Model: "m", Timeout: time.Second, MaxTokens: 64, StructuredOutputMode: "json_schema", JSONSchemaStrict: true}
	if _, _, _, err := c.Do(context.Background(), AuditRequest{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	js := got["response_format"].(map[string]any)["json_schema"].(map[string]any)
	// OpenRouter documents strict as optional-but-recommended
	// (openrouter.ai/docs/guides/features/structured-outputs).
	if js["strict"] != true {
		t.Fatalf("strict not sent: %#v", js)
	}
	schema := js["schema"].(map[string]any)
	props := schema["properties"].(map[string]any)
	if _, hasRefusal := props["refusal"]; hasRefusal {
		t.Fatal("refusal property must not be requested")
	}
	if required := schema["required"].([]any); len(required) != 2 {
		t.Fatalf("required=%v", required)
	}
}
func TestClientNoDegradeStatuses(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(status) }))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL, StructuredOutputMode: "auto", Timeout: time.Second}
			_, _, _, _ = c.Do(context.Background(), AuditRequest{Text: "x"})
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestEndpointURLPreservesBasePath(t *testing.T) {
	cases := map[string]string{
		// Zhipu test endpoint: base keeps /api/paas/v4 (docs.bigmodel.cn Chat Completions OpenAPI)
		"https://open.bigmodel.cn/api/paas/v4": "https://open.bigmodel.cn/api/paas/v4/chat/completions",
		// OpenRouter: base keeps /api/v1 (openrouter.ai/docs)
		"https://openrouter.ai/api/v1": "https://openrouter.ai/api/v1/chat/completions",
		"https://api.example.com":      "https://api.example.com/chat/completions",
		"https://api.example.com/v1/":  "https://api.example.com/v1/chat/completions",
	}
	for base, want := range cases {
		if got := endpointURL(base); got != want {
			t.Errorf("endpointURL(%q) = %q, want %q", base, got, want)
		}
	}
}
func TestJoinContent(t *testing.T) {
	got, err := joinContent(json.RawMessage(`[{"type":"text","text":"one"},{"type":"text","text":"two"}]`))
	if err != nil || got != "one\ntwo" {
		t.Fatalf("%q %v", got, err)
	}
}

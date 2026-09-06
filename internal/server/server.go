package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"llm2qwen3guard/internal/config"
	"llm2qwen3guard/internal/qwen3guard"
	"llm2qwen3guard/internal/upstream"
)

// Handler exposes the OpenAI-compatible prompt-audit endpoints described in
// DESIGN.md section 4. Response auditing is intentionally not implemented:
// sub2api's openspec prompt-input-audit and prompt_snapshot.go are prompt-only.
type Handler struct {
	cfg      config.Config
	upstream *upstream.Client
}

// New creates a gateway HTTP handler from validated configuration.
func New(cfg config.Config) *Handler {
	return &Handler{cfg: cfg, upstream: &upstream.Client{BaseURL: cfg.UpstreamBaseURL, APIKey: cfg.UpstreamAPIKey, Model: cfg.UpstreamModel, Timeout: time.Duration(cfg.UpstreamTimeout) * time.Second, MaxTokens: cfg.UpstreamMaxTokens, Temperature: cfg.UpstreamTemperature, StructuredOutputMode: cfg.StructuredOutputMode, JSONSchemaStrict: cfg.JSONSchemaStrict, ExtraBody: cfg.UpstreamExtraBody}}
}

// NewWithClient creates a handler with an injected upstream client for tests.
func NewWithClient(cfg config.Config, client *upstream.Client) *Handler {
	h := New(cfg)
	if client != nil {
		h.upstream = client
	}
	return h
}

// ServeHTTP routes /healthz and /v1/chat/completions.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	case "/v1/chat/completions":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.chat(w, r)
	default:
		http.NotFound(w, r)
	}
}

type chatRequest struct {
	Messages []qwen3guard.Message `json:"messages"`
	Stream   bool                 `json:"stream"`
}

func (h *Handler) chat(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	upstreamMode, outcome := "", "failure"
	defer func() {
		log.Printf("mode=prompt upstream_mode=%s latency=%s outcome=%s", upstreamMode, time.Since(start).Round(time.Millisecond), outcome)
	}()
	if h.cfg.GatewayAPIKey != "" && r.Header.Get("Authorization") != "Bearer "+h.cfg.GatewayAPIKey {
		writeAPIError(w, http.StatusUnauthorized, "invalid gateway api key")
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Messages) == 0 {
		writeAPIError(w, http.StatusBadRequest, "messages must not be empty")
		return
	}
	text, err := qwen3guard.ExtractAuditText(req.Messages)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.cfg.MaxInputChars > 0 {
		runes := []rune(text)
		if len(runes) > h.cfg.MaxInputChars {
			log.Printf("input truncated from %d to %d characters", len(runes), h.cfg.MaxInputChars)
			text = string(runes[:h.cfg.MaxInputChars])
		}
	}
	content, usage, upstreamMode, err := h.upstream.Do(r.Context(), upstream.AuditRequest{Text: text})
	if err != nil {
		h.failure(w, err, req.Stream)
		return
	}
	v, err := qwen3guard.ParseUpstreamJSON(content)
	if err == nil {
		v, err = qwen3guard.ValidateVerdict(v)
	}
	if err != nil {
		h.failure(w, err, req.Stream)
		return
	}
	outcome = "success"
	h.writeCompletion(w, req.Stream, qwen3guard.Render(v.Safety, v.Categories), usage)
}
func (h *Handler) failure(w http.ResponseWriter, cause error, stream bool) {
	if h.cfg.FailurePolicy == "safe" {
		h.writeCompletion(w, stream, qwen3guard.Render(qwen3guard.SafetySafe, nil), nil)
		return
	}
	if h.cfg.FailurePolicy == "unsafe" {
		h.writeCompletion(w, stream, qwen3guard.Render(qwen3guard.SafetyUnsafe, nil), nil)
		return
	}
	writeAPIError(w, http.StatusServiceUnavailable, fmt.Sprintf("guard pipeline failure: %v", cause))
}
func (h *Handler) writeCompletion(w http.ResponseWriter, stream bool, content string, usage json.RawMessage) {
	id := qwen3guard.NewRequestID()
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		role := map[string]any{"id": id, "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}}
		full := map[string]any{"id": id, "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}, "finish_reason": nil}}}
		for _, frame := range []any{role, full} {
			data, _ := json.Marshal(frame)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	envelope := map[string]any{"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": "qwen3guard", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}}
	if len(usage) > 0 && string(usage) != "null" {
		var u any
		if json.Unmarshal(usage, &u) == nil {
			envelope["usage"] = u
		}
	}
	_ = json.NewEncoder(w).Encode(envelope)
}
func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message, "type": "api_error", "code": "guard_pipeline_failure"}})
}

// Config returns the handler's effective configuration.
func (h *Handler) Config() config.Config { return h.cfg }

var _ http.Handler = (*Handler)(nil)

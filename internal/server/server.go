package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"llm2qwen3guard/internal/config"
	"llm2qwen3guard/internal/logsys"
	"llm2qwen3guard/internal/qwen3guard"
	"llm2qwen3guard/internal/upstream"
)

// defaultMaxRequestBytes caps the inbound JSON envelope when MAX_REQUEST_BYTES
// is unset (see chat). 1MiB comfortably fits sub2api's largest chunk
// (MaxInputLimit=100000 chars, backend/internal/securityaudit/prompt_config.go,
// https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/backend/internal/securityaudit/prompt_config.go)
// with JSON-escaping overhead.
const defaultMaxRequestBytes = 1 << 20

// Handler exposes the OpenAI-compatible prompt-audit endpoints described in
// DESIGN.md section 4. Response auditing is intentionally not implemented:
// sub2api's openspec prompt-input-audit and prompt_snapshot.go are prompt-only.
type Handler struct {
	cfg      config.Config
	upstream *upstream.Client
	log      *logsys.Logger
}

// New creates a gateway HTTP handler from validated configuration. The logger
// may be nil (logging disabled); New installs a stdout-only logger otherwise.
func New(cfg config.Config, lg *logsys.Logger) *Handler {
	if lg == nil {
		lg, _ = logsys.New("", "info")
	}
	return &Handler{cfg: cfg, upstream: upstream.NewClient(cfg), log: lg}
}

// NewWithClient creates a handler with an injected upstream client for tests.
func NewWithClient(cfg config.Config, client *upstream.Client, lg *logsys.Logger) *Handler {
	h := New(cfg, lg)
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
	reqID := qwen3guard.NewRequestID()
	// Log field whitelist (audit-relevant, non-sensitive): request_id,
	// text_chars, stream, model, base_url, api_key (REDACTED), mode, status,
	// latency_ms, safety, categories. The audited text, messages, raw
	// upstream JSON, and the policy appendix are NEVER logged.
	fields := map[string]any{
		"request_id": reqID,
		"model":      h.cfg.UpstreamModel,
		"base_url":   h.cfg.UpstreamBaseURL,
		"api_key":    logsys.Redact(h.cfg.UpstreamAPIKey),
		"stream":     false,
	}
	upstreamMode, verdict := "", ""
	defer func() {
		fields["mode"] = upstreamMode
		fields["latency_ms"] = time.Since(start).Milliseconds()
		if verdict != "" {
			fields["safety"], fields["categories"] = splitVerdict(verdict)
			h.log.Infof("audit", fields)
		} else {
			h.log.Errorf("audit_failed", fields)
		}
	}()
	if h.cfg.GatewayAPIKey != "" && r.Header.Get("Authorization") != "Bearer "+h.cfg.GatewayAPIKey {
		fields["status"] = 401
		writeAPIError(w, http.StatusUnauthorized, "invalid gateway api key")
		return
	}
	// Bound the request body before decoding: MAX_INPUT_CHARS bounds the
	// audited text, but the JSON envelope itself must also be capped so a
	// hostile client cannot stream an unbounded body at the gateway.
	limit := int64(h.cfg.MaxRequestBytes)
	if limit <= 0 {
		limit = defaultMaxRequestBytes
	}
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			fields["status"] = 413
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request body exceeds limit")
			return
		}
		fields["status"] = 400
		writeAPIError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	fields["stream"] = req.Stream
	if len(req.Messages) == 0 {
		fields["status"] = 400
		writeAPIError(w, http.StatusBadRequest, "messages must not be empty")
		return
	}
	text, err := qwen3guard.ExtractAuditText(req.Messages)
	if err != nil {
		fields["status"] = 400
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.cfg.MaxInputChars > 0 {
		runes := []rune(text)
		if len(runes) > h.cfg.MaxInputChars {
			h.log.Warnf("input_truncated", map[string]any{"request_id": reqID, "from_chars": len(runes), "to_chars": h.cfg.MaxInputChars})
			text = string(runes[:h.cfg.MaxInputChars])
		}
	}
	fields["text_chars"] = len([]rune(text))
	content, usage, upstreamMode, err := h.upstream.Do(r.Context(), upstream.AuditRequest{Text: text})
	if err != nil {
		h.failure(w, err, req.Stream, fields)
		return
	}
	v, err := qwen3guard.ParseUpstreamJSON(content)
	if err == nil {
		v, err = qwen3guard.ValidateVerdict(v)
	}
	if err != nil {
		h.failure(w, err, req.Stream, fields)
		return
	}
	fields["status"] = 200
	verdict = v.Safety + "/" + strings.Join(v.Categories, ",")
	h.writeCompletion(w, req.Stream, qwen3guard.Render(v.Safety, v.Categories), usage, reqID)
}

// splitVerdict decomposes "Safety/Cat1,Cat2" for structured logging.
func splitVerdict(v string) (string, string) {
	if i := strings.IndexByte(v, '/'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}
func (h *Handler) failure(w http.ResponseWriter, cause error, stream bool, fields map[string]any) {
	if h.cfg.FailurePolicy == "safe" {
		fields["status"], fields["safety"] = 200, "Safe"
		h.writeCompletion(w, stream, qwen3guard.Render(qwen3guard.SafetySafe, nil), nil, fields["request_id"].(string))
		return
	}
	if h.cfg.FailurePolicy == "unsafe" {
		fields["status"], fields["safety"] = 200, "Unsafe"
		h.writeCompletion(w, stream, qwen3guard.Render(qwen3guard.SafetyUnsafe, nil), nil, fields["request_id"].(string))
		return
	}
	// Stable generic message only: the underlying error can embed the raw
	// upstream response body (UpstreamError.Err), which must never reach the
	// caller (project non-goal: no upstream JSON/explanations to sub2api).
	// Details stay in the structured log via audit_failed fields.
	fields["status"] = 503
	writeAPIError(w, http.StatusServiceUnavailable, "guard pipeline failure")
}
func (h *Handler) writeCompletion(w http.ResponseWriter, stream bool, content string, usage json.RawMessage, id string) {
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// Standard OpenAI stream shape: role delta, content delta, then a
		// terminal chunk with finish_reason "stop" (sub2api's OpenAI clients
		// expect the stop marker to terminate a stream).
		frames := []map[string]any{
			{"id": id, "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}},
			{"id": id, "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}, "finish_reason": nil}}},
			{"id": id, "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}},
		}
		for _, frame := range frames {
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

var _ http.Handler = (*Handler)(nil)

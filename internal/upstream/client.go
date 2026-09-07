package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"llm2qwen3guard/internal/config"
	"llm2qwen3guard/internal/qwen3guard"
)

// maxResponseBytes caps the upstream response body, mirroring sub2api's
// maxGuardResponseBytes (Wei-Shaw/sub2api backend/internal/securityaudit/
// prompt_outbound_security.go,
// https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/backend/internal/securityaudit/prompt_outbound_security.go).
const maxResponseBytes = 256 * 1024

// maxErrorSnippetBytes caps how much of an upstream error body is retained in
// the error message.
const maxErrorSnippetBytes = 4096

// UpstreamError retains the HTTP status and the structured-output mode for the
// gateway failure policy; the degradation decision reads Status directly.
type UpstreamError struct {
	Status int
	Mode   string
	Err    error
}

func (e *UpstreamError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("upstream %s (%d): %v", e.Mode, e.Status, e.Err)
	}
	return fmt.Sprintf("upstream %s (%d)", e.Mode, e.Status)
}
func (e *UpstreamError) Unwrap() error { return e.Err }

// Usage is the upstream usage object preserved in the OpenAI response envelope.
type Usage = json.RawMessage

// AuditRequest describes one prompt classification call.
type AuditRequest struct{ Text string }

// Client invokes an OpenAI-compatible upstream endpoint using structured output:
// json_schema per OpenRouter (https://openrouter.ai/docs/guides/features/structured-outputs)
// and SiliconFlow (https://docs.siliconflow.com/cn/userguide/guides/json-mode_struct),
// json_object per both providers' JSON-mode docs (e.g. Zhipu only supports
// json_object: https://docs.bigmodel.cn/cn/guide/capabilities/struct-output).
type Client struct {
	BaseURL              string
	APIKey               string
	Model                string
	Timeout              time.Duration
	MaxTokens            int
	Temperature          float64
	StructuredOutputMode string
	// PolicyAppendix is the operator-configured audit focus appended to the
	// system policy (see qwen3guard.SystemPolicy); empty = stock policy.
	PolicyAppendix string
	// JSONSchemaStrict adds "strict": true to json_schema requests; optional
	// per OpenRouter, undocumented for SiliconFlow (see config.Config).
	JSONSchemaStrict bool
	ExtraBody        map[string]any
	HTTPClient       *http.Client
}

// NewClient builds the production client from validated gateway configuration.
func NewClient(cfg config.Config) *Client {
	return &Client{
		BaseURL:              cfg.UpstreamBaseURL,
		APIKey:               cfg.UpstreamAPIKey,
		Model:                cfg.UpstreamModel,
		Timeout:              time.Duration(cfg.UpstreamTimeout) * time.Second,
		MaxTokens:            cfg.UpstreamMaxTokens,
		Temperature:          cfg.UpstreamTemperature,
		StructuredOutputMode: cfg.StructuredOutputMode,
		PolicyAppendix:       cfg.PolicyAppendix,
		JSONSchemaStrict:     cfg.JSONSchemaStrict,
		ExtraBody:            cfg.UpstreamExtraBody,
	}
}

// Do performs json_schema, json_object, or the sole auto degradation chain and
// returns content, usage, and the structured output mode used.
func (c *Client) Do(ctx context.Context, req AuditRequest) (string, Usage, string, error) {
	outputMode := strings.ToLower(strings.TrimSpace(c.StructuredOutputMode))
	if outputMode == "" {
		outputMode = "auto"
	}
	modes := []string{outputMode}
	if outputMode == "auto" {
		modes = []string{"json_schema", "json_object"}
	}
	var last error
	for i, om := range modes {
		content, usage, err := c.doOne(ctx, req.Text, om)
		if err == nil {
			return content, usage, om, nil
		}
		last = err
		ue, ok := err.(*UpstreamError)
		if !ok || outputMode != "auto" || i == len(modes)-1 || ue.Status == 0 || ue.Status == 401 || ue.Status == 403 || ue.Status == 429 || ue.Status < 400 || ue.Status >= 500 {
			break
		}
	}
	return "", nil, "", last
}
func (c *Client) doOne(ctx context.Context, text, outputMode string) (string, Usage, error) {
	body := map[string]any{"model": c.Model, "messages": []map[string]string{{"role": "system", "content": qwen3guard.SystemPolicy(c.PolicyAppendix)}, {"role": "user", "content": text}}, "temperature": c.Temperature, "max_tokens": c.MaxTokens, "response_format": responseFormat(outputMode, c.JSONSchemaStrict)}
	for k, v := range c.ExtraBody {
		body[k] = v
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", nil, &UpstreamError{Mode: outputMode, Err: err}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	hreq, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpointURL(c.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return "", nil, &UpstreamError{Mode: outputMode, Err: err}
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		return "", nil, &UpstreamError{Mode: outputMode, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorSnippetBytes))
		return "", nil, &UpstreamError{Status: resp.StatusCode, Mode: outputMode, Err: fmt.Errorf("%s", strings.TrimSpace(string(b)))}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", nil, &UpstreamError{Status: resp.StatusCode, Mode: outputMode, Err: err}
	}
	if len(data) > maxResponseBytes {
		return "", nil, &UpstreamError{Status: resp.StatusCode, Mode: outputMode, Err: fmt.Errorf("upstream response exceeds %d bytes", maxResponseBytes)}
	}
	var env struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return "", nil, &UpstreamError{Status: resp.StatusCode, Mode: outputMode, Err: err}
	}
	if len(env.Choices) == 0 {
		return "", nil, &UpstreamError{Status: resp.StatusCode, Mode: outputMode, Err: fmt.Errorf("missing choices")}
	}
	content, err := joinContent(env.Choices[0].Message.Content)
	if err != nil {
		return "", nil, &UpstreamError{Status: resp.StatusCode, Mode: outputMode, Err: err}
	}
	return content, env.Usage, nil
}

// endpointURL appends "/chat/completions" while preserving the configured base
// path verbatim (OpenAI SDK convention): e.g. Zhipu base
// https://open.bigmodel.cn/api/paas/v4 (docs.bigmodel.cn Chat Completions API)
// and OpenRouter base https://openrouter.ai/api/v1 both POST to
// base + "/chat/completions".
func endpointURL(base string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/") + "/chat/completions"
}

// responseFormat builds the response_format request field: json_object shape
// per https://docs.siliconflow.com/cn/userguide/guides/json-mode and
// https://docs.bigmodel.cn/cn/guide/capabilities/struct-output; json_schema
// shape per https://openrouter.ai/docs/guides/features/structured-outputs and
// https://docs.siliconflow.com/cn/userguide/guides/json-mode_struct. "strict"
// is only included when explicitly enabled (OpenRouter: optional;
// SiliconFlow: undocumented under response_format).
func responseFormat(output string, strict bool) map[string]any {
	if output == "json_object" {
		return map[string]any{"type": "json_object"}
	}
	js := map[string]any{"name": "qwen3guard_verdict", "schema": map[string]any{"type": "object", "properties": schemaProperties(), "required": []string{"safety", "categories"}, "additionalProperties": false}}
	if strict {
		js["strict"] = true
	}
	return map[string]any{"type": "json_schema", "json_schema": js}
}

func schemaProperties() map[string]any {
	// NOTE: no "uniqueItems" on the categories array — DashScope (Qwen)
	// rejects json_schema whose array types carry uniqueItems
	// ("<400> InternalError.Algo.InvalidParameter ... When the schema contains
	// the fields \"uniqueItems\" ... the type should not be \"array\"").
	// Deduplication is enforced locally by NormalizeUpstreamCategories, so the
	// schema constraint is unnecessary.
	return map[string]any{"safety": map[string]any{"type": "string", "enum": []string{qwen3guard.SafetySafe, qwen3guard.SafetyUnsafe, qwen3guard.SafetyControversial}}, "categories": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": qwen3guard.PromptCategories}}}
}
func joinContent(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.TrimSpace(s) == "" {
			return "", fmt.Errorf("empty content")
		}
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("invalid content: %w", err)
	}
	lines := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Type == "text" || p.Type == "" {
			lines = append(lines, p.Text)
		}
	}
	out := strings.Join(lines, "\n")
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("empty content")
	}
	return out, nil
}

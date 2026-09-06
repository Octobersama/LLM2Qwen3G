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

	"llm2qwen3guard/internal/qwen3guard"
)

// ErrorKind identifies whether an upstream failure is authentication,
// non-retryable, potentially degradable, or transport-related.
type ErrorKind string

const (
	ErrorAuth         ErrorKind = "auth"
	ErrorNonRetryable ErrorKind = "nonretryable"
	ErrorDegraded     ErrorKind = "degraded-possible"
	ErrorTransport    ErrorKind = "transport"
)

// UpstreamError retains status and classification for the gateway failure policy.
type UpstreamError struct {
	Kind   ErrorKind
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

// Client invokes an OpenAI-compatible upstream endpoint using structured output
// negotiation from DESIGN.md section 3.1-3.2.
type Client struct {
	BaseURL              string
	APIKey               string
	Model                string
	Timeout              time.Duration
	MaxTokens            int
	Temperature          float64
	StructuredOutputMode string
	JSONSchemaStrict     bool
	ExtraBody            map[string]any
	HTTPClient           *http.Client
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
	body := map[string]any{"model": c.Model, "messages": []map[string]string{{"role": "system", "content": qwen3guard.PromptSystemPolicy}, {"role": "user", "content": text}}, "temperature": c.Temperature, "max_tokens": c.MaxTokens, "response_format": responseFormat(outputMode, c.JSONSchemaStrict)}
	for k, v := range c.ExtraBody {
		body[k] = v
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", nil, &UpstreamError{Kind: ErrorNonRetryable, Mode: outputMode, Err: err}
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
		return "", nil, &UpstreamError{Kind: ErrorNonRetryable, Mode: outputMode, Err: err}
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		return "", nil, &UpstreamError{Kind: ErrorTransport, Mode: outputMode, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		kind := ErrorNonRetryable
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			kind = ErrorAuth
		} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			kind = ErrorDegraded
		}
		return "", nil, &UpstreamError{Kind: kind, Status: resp.StatusCode, Mode: outputMode, Err: fmt.Errorf("%s", strings.TrimSpace(string(b)))}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024+1))
	if err != nil {
		return "", nil, &UpstreamError{Kind: ErrorTransport, Status: resp.StatusCode, Mode: outputMode, Err: err}
	}
	if len(data) > 256*1024 {
		return "", nil, &UpstreamError{Kind: ErrorNonRetryable, Status: resp.StatusCode, Mode: outputMode, Err: fmt.Errorf("upstream response exceeds 256KB")}
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
		return "", nil, &UpstreamError{Kind: ErrorNonRetryable, Status: resp.StatusCode, Mode: outputMode, Err: err}
	}
	if len(env.Choices) == 0 {
		return "", nil, &UpstreamError{Kind: ErrorNonRetryable, Status: resp.StatusCode, Mode: outputMode, Err: fmt.Errorf("missing choices")}
	}
	content, err := joinContent(env.Choices[0].Message.Content)
	if err != nil {
		return "", nil, &UpstreamError{Kind: ErrorNonRetryable, Status: resp.StatusCode, Mode: outputMode, Err: err}
	}
	return content, env.Usage, nil
}

// endpointURL appends "/chat/completions" while preserving provider base paths.
func endpointURL(base string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/") + "/chat/completions"
}

// responseFormat builds the documented json_object or json_schema request shape.
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
	return map[string]any{"safety": map[string]any{"type": "string", "enum": []string{qwen3guard.SafetySafe, qwen3guard.SafetyUnsafe, qwen3guard.SafetyControversial}}, "categories": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": qwen3guard.PromptCategories}, "uniqueItems": true}}
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

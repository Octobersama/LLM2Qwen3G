package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config contains gateway runtime settings. Environment names and defaults are
// defined by DESIGN.md section 4/5.
type Config struct {
	ListenAddr           string
	UpstreamBaseURL      string
	UpstreamAPIKey       string
	UpstreamModel        string
	UpstreamTimeout      int
	UpstreamMaxTokens    int
	UpstreamTemperature  float64
	StructuredOutputMode string
	// JSONSchemaStrict includes "strict": true in json_schema requests.
	// OpenRouter documents strict as optional-but-recommended
	// (https://openrouter.ai/docs/guides/features/structured-outputs);
	// SiliconFlow does not document a strict field under response_format
	// (https://docs.siliconflow.com/cn/userguide/guides/json-mode_struct)
	// so it defaults off and is an explicit opt-in.
	JSONSchemaStrict  bool
	UpstreamExtraBody map[string]any
	MaxInputChars     int
	// MaxRequestBytes caps the inbound /v1/chat/completions JSON envelope
	// (MAX_REQUEST_BYTES, default 1MiB).
	MaxRequestBytes int
	// PolicyAppendix is the operator-configured audit focus text loaded from
	// AUDIT_POLICY_APPEND_FILE and inserted into the system policy (see
	// qwen3guard.SystemPolicy). Empty = stock Qwen3Guard policy only.
	PolicyAppendix string
	// LogDir targets the structured file log when set (LOG_DIR; default
	// "logs" under the working directory; "off" disables file logging —
	// stdout logging always remains on for Docker/journald consumption).
	LogDir        string
	LogLevel      string
	FailurePolicy string
	GatewayAPIKey string
}

// FromEnv reads and validates gateway configuration from environment variables.
func FromEnv() (Config, error) {
	c := Config{
		ListenAddr: ":8080", UpstreamTimeout: 30, UpstreamMaxTokens: 128,
		UpstreamTemperature: 0, StructuredOutputMode: "auto",
		MaxInputChars: 32000, MaxRequestBytes: 1 << 20,
		FailurePolicy: "error", UpstreamExtraBody: map[string]any{},
	}
	c.ListenAddr = envOr("LISTEN_ADDR", c.ListenAddr)
	c.UpstreamBaseURL = strings.TrimSpace(os.Getenv("UPSTREAM_BASE_URL"))
	c.UpstreamAPIKey = strings.TrimSpace(os.Getenv("UPSTREAM_API_KEY"))
	c.UpstreamModel = strings.TrimSpace(os.Getenv("UPSTREAM_MODEL"))
	if c.UpstreamBaseURL == "" || c.UpstreamAPIKey == "" || c.UpstreamModel == "" {
		return Config{}, fmt.Errorf("UPSTREAM_BASE_URL, UPSTREAM_API_KEY, and UPSTREAM_MODEL are required")
	}
	var err error
	if c.UpstreamTimeout, err = envInt("UPSTREAM_TIMEOUT_SECONDS", c.UpstreamTimeout); err != nil {
		return Config{}, err
	}
	if c.UpstreamMaxTokens, err = envInt("UPSTREAM_MAX_TOKENS", c.UpstreamMaxTokens); err != nil {
		return Config{}, err
	}
	if c.UpstreamTemperature, err = envFloat("UPSTREAM_TEMPERATURE", c.UpstreamTemperature); err != nil {
		return Config{}, err
	}
	if c.MaxInputChars, err = envInt("MAX_INPUT_CHARS", c.MaxInputChars); err != nil {
		return Config{}, err
	}
	c.StructuredOutputMode = strings.ToLower(envOr("STRUCTURED_OUTPUT_MODE", c.StructuredOutputMode))
	if c.MaxRequestBytes, err = envInt("MAX_REQUEST_BYTES", c.MaxRequestBytes); err != nil {
		return Config{}, err
	}
	c.FailurePolicy = strings.ToLower(envOr("FAILURE_POLICY", c.FailurePolicy))
	if raw := strings.TrimSpace(os.Getenv("UPSTREAM_EXTRA_BODY_JSON")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &c.UpstreamExtraBody); err != nil {
			return Config{}, fmt.Errorf("UPSTREAM_EXTRA_BODY_JSON: %w", err)
		}
		if c.UpstreamExtraBody == nil {
			c.UpstreamExtraBody = map[string]any{}
		}
	}
	if raw := strings.TrimSpace(os.Getenv("UPSTREAM_JSON_SCHEMA_STRICT")); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid UPSTREAM_JSON_SCHEMA_STRICT %q", raw)
		}
		c.JSONSchemaStrict = b
	}
	if c.UpstreamTimeout <= 0 || c.UpstreamMaxTokens <= 0 || c.UpstreamTemperature < 0 || c.UpstreamTemperature > 2 || c.MaxInputChars < 0 || c.MaxRequestBytes <= 0 {
		return Config{}, fmt.Errorf("invalid numeric configuration")
	}
	if c.StructuredOutputMode != "auto" && c.StructuredOutputMode != "json_schema" && c.StructuredOutputMode != "json_object" {
		return Config{}, fmt.Errorf("invalid STRUCTURED_OUTPUT_MODE")
	}
	if c.FailurePolicy != "error" && c.FailurePolicy != "safe" && c.FailurePolicy != "unsafe" {
		return Config{}, fmt.Errorf("invalid FAILURE_POLICY")
	}
	// Operator-configured audit focus: loaded once at startup from
	// AUDIT_POLICY_APPEND_FILE; empty/unset = stock policy.
	if p := strings.TrimSpace(os.Getenv("AUDIT_POLICY_APPEND_FILE")); p != "" {
		data, err := os.ReadFile(p)
		if err != nil {
			return Config{}, fmt.Errorf("AUDIT_POLICY_APPEND_FILE: %w", err)
		}
		c.PolicyAppendix = string(data)
	}
	// Logging: LOG_DIR default "logs" (same directory); "off" disables the
	// file sink, stdout always stays on. LOG_LEVEL in debug|info|warn|error.
	c.LogDir = envOr("LOG_DIR", "logs")
	c.LogLevel = strings.ToLower(envOr("LOG_LEVEL", "info"))
	if c.LogDir != "off" {
		// fail fast when the target is not writable
		if err := os.MkdirAll(c.LogDir, 0o755); err != nil {
			return Config{}, fmt.Errorf("LOG_DIR %q: %w", c.LogDir, err)
		}
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("invalid LOG_LEVEL %q", c.LogLevel)
	}
	c.GatewayAPIKey = os.Getenv("GATEWAY_API_KEY")
	return c, nil
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
func envInt(name string, fallback int) (int, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}
func envFloat(name string, fallback float64) (float64, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}

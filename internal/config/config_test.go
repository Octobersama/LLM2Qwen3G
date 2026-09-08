package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validEnv sets the required trio plus safe defaults for a successful parse.
// It clears every optional variable to isolate tests from host/CI
// environment presets (empty value makes FromEnv apply the default).
func validEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"LISTEN_ADDR", "UPSTREAM_TIMEOUT_SECONDS", "UPSTREAM_MAX_TOKENS",
		"UPSTREAM_TEMPERATURE", "STRUCTURED_OUTPUT_MODE",
		"UPSTREAM_JSON_SCHEMA_STRICT", "UPSTREAM_EXTRA_BODY_JSON",
		"MAX_INPUT_CHARS", "MAX_REQUEST_BYTES", "AUDIT_POLICY_APPEND_FILE",
		"LOG_LEVEL", "FAILURE_POLICY", "GATEWAY_API_KEY",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("UPSTREAM_BASE_URL", "https://api.example.com/v1")
	t.Setenv("UPSTREAM_API_KEY", "sk-test")
	t.Setenv("UPSTREAM_MODEL", "m")
	t.Setenv("LOG_DIR", "off") // never touch the working directory in tests
}

func TestRequiredTrio(t *testing.T) {
	for _, tc := range []struct{ name, unset string }{
		{"missing base url", "UPSTREAM_BASE_URL"},
		{"missing api key", "UPSTREAM_API_KEY"},
		{"missing model", "UPSTREAM_MODEL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validEnv(t)
			t.Setenv(tc.unset, "  ") // TrimSpace -> empty
			_, err := FromEnv()
			if err == nil || !strings.Contains(err.Error(), "required") {
				t.Fatalf("want required error, got %v", err)
			}
		})
	}
}

func TestEnumValidation(t *testing.T) {
	for _, tc := range []struct {
		name, env, value string
	}{
		{"bad structured mode", "STRUCTURED_OUTPUT_MODE", "yaml"},
		{"bad failure policy", "FAILURE_POLICY", "maybe"},
		{"bad log level", "LOG_LEVEL", "verbose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validEnv(t)
			t.Setenv(tc.env, tc.value)
			if _, err := FromEnv(); err == nil {
				t.Fatalf("%s=%q must be rejected", tc.env, tc.value)
			}
		})
	}
	// All legal enum values parse.
	for _, mode := range []string{"auto", "json_schema", "json_object"} {
		for _, policy := range []string{"error", "safe", "unsafe"} {
			validEnv(t)
			t.Setenv("STRUCTURED_OUTPUT_MODE", mode)
			t.Setenv("FAILURE_POLICY", policy)
			c, err := FromEnv()
			if err != nil || c.StructuredOutputMode != mode || c.FailurePolicy != policy {
				t.Fatalf("mode=%s policy=%s: %v", mode, policy, err)
			}
		}
	}
}

func TestNumericBounds(t *testing.T) {
	for _, tc := range []struct {
		name, env, value string
	}{
		{"timeout zero", "UPSTREAM_TIMEOUT_SECONDS", "0"},
		{"timeout negative", "UPSTREAM_TIMEOUT_SECONDS", "-1"},
		{"max tokens zero", "UPSTREAM_MAX_TOKENS", "0"},
		{"temperature negative", "UPSTREAM_TEMPERATURE", "-0.1"},
		{"temperature too high", "UPSTREAM_TEMPERATURE", "2.5"},
		{"input chars negative", "MAX_INPUT_CHARS", "-1"},
		{"request bytes zero", "MAX_REQUEST_BYTES", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validEnv(t)
			t.Setenv(tc.env, tc.value)
			if _, err := FromEnv(); err == nil {
				t.Fatalf("%s=%q must be rejected", tc.env, tc.value)
			}
		})
	}
}

func TestDefaultsAndOverrides(t *testing.T) {
	validEnv(t)
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" || c.UpstreamTimeout != 30 || c.UpstreamMaxTokens != 128 ||
		c.UpstreamTemperature != 0 || c.MaxInputChars != 32000 || c.MaxRequestBytes != 1<<20 ||
		c.StructuredOutputMode != "auto" || c.FailurePolicy != "error" || c.LogLevel != "info" {
		t.Fatalf("defaults drifted: %+v", c)
	}

	t.Setenv("MAX_REQUEST_BYTES", "2048")
	t.Setenv("UPSTREAM_TIMEOUT_SECONDS", "45")
	c, err = FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxRequestBytes != 2048 || c.UpstreamTimeout != 45 {
		t.Fatalf("env override ignored: %+v", c)
	}
}

func TestPolicyAppendixFile(t *testing.T) {
	validEnv(t)
	t.Setenv("AUDIT_POLICY_APPEND_FILE", filepath.Join(t.TempDir(), "missing.txt"))
	if _, err := FromEnv(); err == nil {
		t.Fatal("missing appendix file must fail fast")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "focus.md")
	if err := os.WriteFile(path, []byte("focus on PII"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUDIT_POLICY_APPEND_FILE", path)
	c, err := FromEnv()
	if err != nil || c.PolicyAppendix != "focus on PII" {
		t.Fatalf("appendix not loaded: %+v err=%v", c, err)
	}
}

func TestExtraBodyJSON(t *testing.T) {
	validEnv(t)
	t.Setenv("UPSTREAM_EXTRA_BODY_JSON", `{"thinking":{"type":"disabled"}}`)
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	body, ok := c.UpstreamExtraBody["thinking"].(map[string]any)
	if !ok || body["type"] != "disabled" {
		t.Fatalf("extra body not parsed: %#v", c.UpstreamExtraBody)
	}

	t.Setenv("UPSTREAM_EXTRA_BODY_JSON", `{invalid`)
	if _, err := FromEnv(); err == nil {
		t.Fatal("invalid JSON must be rejected")
	}
}

func TestLogDirOffSkipsMkdir(t *testing.T) {
	validEnv(t)
	t.Setenv("LOG_DIR", "off")
	c, err := FromEnv()
	if err != nil || c.LogDir != "off" {
		t.Fatalf("LOG_DIR=off rejected: %+v err=%v", c, err)
	}

	// Unwritable target: a file path (not directory) reused as LOG_DIR makes
	// MkdirAll fail — asserting the fail-fast branch without NUL bytes that
	// Windows setenv rejects.
	file := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOG_DIR", filepath.Join(file, "sub"))
	if _, err := FromEnv(); err == nil {
		t.Fatal("unwritable LOG_DIR must fail")
	}
}

func TestStrictBool(t *testing.T) {
	validEnv(t)
	t.Setenv("UPSTREAM_JSON_SCHEMA_STRICT", "true")
	c, err := FromEnv()
	if err != nil || !c.JSONSchemaStrict {
		t.Fatalf("strict=true not applied: %+v err=%v", c, err)
	}
	t.Setenv("UPSTREAM_JSON_SCHEMA_STRICT", "yes")
	if _, err := FromEnv(); err == nil {
		t.Fatal("non-boolean strict must be rejected")
	}
}

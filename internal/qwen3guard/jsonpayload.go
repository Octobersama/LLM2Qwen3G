package qwen3guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ParseUpstreamJSON parses and validates structured upstream content. It accepts
// fenced JSON and surrounding prose for compatibility with providers that wrap
// json_object output, while always applying local validation from DESIGN.md 3.2.
func ParseUpstreamJSON(content string) (Verdict, error) {
	var raw struct {
		Safety     string   `json:"safety"`
		Categories []string `json:"categories"`
	}
	var object map[string]json.RawMessage
	candidate := strings.TrimSpace(content)
	valid := json.Unmarshal([]byte(candidate), &raw) == nil && json.Unmarshal([]byte(candidate), &object) == nil
	if !valid {
		candidate = extractJSON(content)
		valid = candidate != "" && json.Unmarshal([]byte(candidate), &raw) == nil && json.Unmarshal([]byte(candidate), &object) == nil
	}
	if !valid {
		return Verdict{}, fmt.Errorf("invalid upstream JSON")
	}
	if _, ok := object["safety"]; !ok {
		return Verdict{}, fmt.Errorf("missing safety")
	}
	if _, ok := object["categories"]; !ok || string(object["categories"]) == "null" {
		return Verdict{}, fmt.Errorf("missing categories")
	}
	return Verdict{Safety: raw.Safety, Categories: raw.Categories}, nil
}

func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < start {
		return ""
	}
	return string(bytes.TrimSpace([]byte(s[start : end+1])))
}

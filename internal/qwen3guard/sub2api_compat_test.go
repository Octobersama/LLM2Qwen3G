package qwen3guard

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

// parseSub2APICompat mirrors ParseQwen3Guard in sub2api prompt_qwen3guard.go
// lines 88-179: prefix matching, case-insensitive values, None/N-A skipping,
// duplicate fields rejected, and unknown category hashing.
func parseSub2APICompat(content string) (string, []string, error) {
	var safety, cats string
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "safety:") {
			if safety != "" {
				return "", nil, fmt.Errorf("duplicate safety")
			}
			safety = strings.TrimSpace(line[len("safety:"):])
		} else if strings.HasPrefix(lower, "categories:") {
			if cats != "" {
				return "", nil, fmt.Errorf("duplicate categories")
			}
			cats = strings.TrimSpace(line[len("categories:"):])
		}
	}
	switch strings.ToLower(safety) {
	case "safe":
		safety = "Safe"
	case "unsafe":
		safety = "Unsafe"
	case "controversial":
		safety = "Controversial"
	default:
		return "", nil, fmt.Errorf("invalid safety")
	}
	if cats == "" {
		return "", nil, fmt.Errorf("missing categories")
	}
	out := []string{}
	for _, x := range strings.Split(cats, ",") {
		x = strings.TrimSpace(x)
		if x == "" || strings.EqualFold(x, "none") || strings.EqualFold(x, "n/a") {
			continue
		}
		ok := false
		for _, c := range PromptCategories {
			if strings.EqualFold(x, c) {
				out = append(out, c)
				ok = true
				break
			}
		}
		if !ok {
			d := sha256.Sum256([]byte(strings.ToLower(x)))
			out = append(out, fmt.Sprintf("unknown:%x", d[:8]))
		}
	}
	return safety, out, nil
}
func TestRenderRoundTripSub2API(t *testing.T) {
	for _, s := range []string{"Safe", "Unsafe", "Controversial"} {
		for _, cats := range [][]string{nil, {"Violent"}, {"Violent", "PII", "Jailbreak"}} {
			raw := Render(s, cats)
			got, parsed, err := parseSub2APICompat(raw)
			if err != nil || got != s || len(parsed) != len(cats) {
				t.Fatalf("%q %#v -> %q %#v %v", s, cats, got, parsed, err)
			}
		}
	}
}

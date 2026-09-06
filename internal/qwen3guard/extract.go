package qwen3guard

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Message is the OpenAI chat message subset consumed by the prompt audit gateway.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ExtractAuditText selects the last user message as required by DESIGN.md section
// 4 and the sub2api prompt-input-audit contract (openspec/changes/add-openai-compatible-prompt-audit).
func ExtractAuditText(messages []Message) (string, error) {
	var selected *Message
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(messages[i].Role, "user") {
			selected = &messages[i]
			break
		}
	}
	if selected == nil {
		return "", fmt.Errorf("no auditable user message")
	}
	text, err := contentText(selected.Content)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("auditable text is empty")
	}
	return text, nil
}

func contentText(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("content must be string or text parts: %w", err)
	}
	lines := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Type == "text" || p.Type == "" {
			lines = append(lines, p.Text)
		}
	}
	return strings.Join(lines, "\n"), nil
}

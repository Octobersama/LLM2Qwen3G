package qwen3guard

import (
	"encoding/json"
	"testing"
)

func msg(role string, content any) Message {
	b, _ := json.Marshal(content)
	return Message{Role: role, Content: b}
}
func TestExtractAuditText(t *testing.T) {
	for _, tc := range []struct {
		name, wantText string
		messages       []Message
		wantErr        bool
	}{
		{"string", "hello", []Message{msg("user", "hello")}, false},
		{"array", "one\ntwo", []Message{msg("user", []map[string]string{{"type": "text", "text": "one"}, {"type": "text", "text": "two"}})}, false},
		{"last user wins", "last", []Message{msg("user", "first"), msg("assistant", "answer"), msg("user", "last")}, false},
		{"assistant only", "", []Message{msg("assistant", "answer")}, true},
		{"empty", "", []Message{msg("user", " ")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractAuditText(tc.messages)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || got != tc.wantText {
				t.Fatalf("got %q err=%v", got, err)
			}
		})
	}
}

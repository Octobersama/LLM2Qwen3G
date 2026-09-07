package logsys

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"":                              "",
		"short":                         "*****",
		"sk-ws-H.PDMYRDI.6529.9koVOiue": "sk-ws-…Oiue",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoggerStdoutOnly(t *testing.T) {
	l, err := New("", "info")
	if err != nil {
		t.Fatal(err)
	}
	l.Infof("hello", map[string]any{"k": "v"}) // must not panic without file sink
	l.Close()
}

func TestLoggerFileSinkAndRotationFields(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, "debug")
	if err != nil {
		t.Fatal(err)
	}
	l.Infof("audit", map[string]any{
		"model":   "qwen-flash",
		"api_key": Redact("sk-ws-H.PDMYRDI.6529.abcdefgh1234"),
		"verdict": "Unsafe/Violent",
	})
	l.Close()

	files, _ := filepath.Glob(filepath.Join(dir, "gateway-*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("want 1 log file, got %v", files)
	}
	f, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rec Event
	if err := json.NewDecoder(bufio.NewReader(f)).Decode(&rec); err != nil {
		t.Fatal(err)
	}
	if rec.Msg != "audit" || rec.Fields["model"] != "qwen-flash" {
		t.Fatalf("record = %+v", rec)
	}
	if key, _ := rec.Fields["api_key"].(string); strings.Contains(key, "6529") || !strings.Contains(key, "…") {
		t.Fatalf("api_key not redacted: %q", key)
	}
}

func TestLevelFiltering(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, "warn")
	if err != nil {
		t.Fatal(err)
	}
	l.Infof("dropped", nil)
	l.Warnf("kept", nil)
	l.Close()

	data, _ := os.ReadFile(filepath.Join(dir, "gateway-"+nowDay()+".jsonl"))
	if strings.Contains(string(data), "dropped") {
		t.Fatal("info event below min level leaked to file sink")
	}
	if !strings.Contains(string(data), "kept") {
		t.Fatal("warn event missing from file sink")
	}
}

func nowDay() string { return time.Now().Format("20060102") }

// Package logsys provides the gateway's dual-sink structured logger:
// human-readable lines to stdout (always on — Docker/journald consume this)
// and JSON-lines to a daily-rotated file under LogDir when enabled.
//
// SECURITY: logsys is a generic sink — it does NOT filter field names. The
// audit-event field whitelist (request_id/text_chars/stream/model/base_url/
// api_key/mode/status/latency_ms/safety/categories, api_key only after
// Redact) is a server-layer contract: internal/server constructs every audit
// event from that whitelist alone (see its chat() log-field comment). Adding
// a new audit field there requires updating DESIGN.md section 4 in the same
// change.
//
// Redact() masks API keys; every log call that can carry upstream
// credentials must pass them through Redact. Key material is never written
// verbatim.
package logsys

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Level orders log severities.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	default:
		return "ERROR"
	}
}

// Logger writes to stdout and, when configured, a JSON-lines file sink.
// It is safe for concurrent use.
type Logger struct {
	mu       sync.Mutex
	stdout   io.Writer
	file     io.WriteCloser
	filePath string
	day      string
	minLevel Level
}

// New builds a Logger. dir may be "" (stdout only) or "off"; otherwise the
// directory is created and a gateway-YYYYMMDD.jsonl file is appended to.
// level is one of debug|info|warn|error (default info).
func New(dir, level string) (*Logger, error) {
	l := &Logger{stdout: os.Stdout, minLevel: parseLevel(level)}
	if dir == "" || dir == "off" {
		return l, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("log dir %q: %w", dir, err)
	}
	if err := l.openFile(dir); err != nil {
		return nil, err
	}
	return l, nil
}

// NewDiscard builds a fully silent logger (stdout discarded, no file sink,
// all levels pass the filter). Intended for tests: unlike New("", "error"),
// it also swallows ERROR-level events, so failure-path tests stay quiet.
func NewDiscard() *Logger {
	return &Logger{stdout: io.Discard, minLevel: LevelDebug}
}
func parseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

func (l *Logger) openFile(dir string) error {
	day := time.Now().Format("20060102")
	name := filepath.Join(dir, "gateway-"+day+".jsonl")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("log file %q: %w", name, err)
	}
	l.file, l.filePath, l.day = f, name, day
	return nil
}

// rotateDaily reopens the file sink when the calendar day changes.
func (l *Logger) rotateDaily() {
	today := time.Now().Format("20060102")
	if l.file == nil || today == l.day {
		return
	}
	old := l.file
	_ = old.Close()
	if err := l.openFile(filepath.Dir(l.filePath)); err != nil {
		l.file, l.filePath = nil, ""
		fmt.Fprintf(l.stdout, "logsys: rotation failed: %v\n", err)
	}
}

// Close releases the file sink.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// Redact masks a secret for logging: first 6 and last 4 characters stay
// visible, the middle is replaced with fixed-length dots. Short secrets are
// fully masked.
func Redact(secret string) string {
	s := strings.TrimSpace(secret)
	if len(s) <= 10 {
		return strings.Repeat("*", len(s))
	}
	return s[:6] + "…" + s[len(s)-4:]
}

// Event is one structured audit-relevant log record.
type Event struct {
	Time   string         `json:"ts"`
	Level  string         `json:"level"`
	Msg    string         `json:"msg"`
	Fields map[string]any `json:"fields,omitempty"`
}

// Log writes one event to both sinks. stdout gets a compact key=value line
// with fields sorted by key (map iteration order is random; deterministic
// lines make journalctl/docker-logs output diffable); the file sink gets the
// JSON object.
func (l *Logger) Log(level Level, msg string, fields map[string]any) {
	if level < l.minLevel {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rotateDaily()
	ev := Event{Time: time.Now().Format(time.RFC3339), Level: level.String(), Msg: msg, Fields: fields}

	var sb strings.Builder
	sb.WriteString(ev.Time)
	sb.WriteString(" ")
	sb.WriteString(ev.Level)
	sb.WriteString(" ")
	sb.WriteString(msg)
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		sb.WriteString(" ")
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(fmt.Sprintf("%v", fields[k]))
	}
	fmt.Fprintln(l.stdout, sb.String())

	if l.file != nil {
		if data, err := json.Marshal(ev); err == nil {
			fmt.Fprintln(l.file, string(data))
		}
	}
}

// Debugf/Infof/Warnf/Errorf are convenience wrappers.
func (l *Logger) Debugf(msg string, fields map[string]any) { l.Log(LevelDebug, msg, fields) }
func (l *Logger) Infof(msg string, fields map[string]any)  { l.Log(LevelInfo, msg, fields) }
func (l *Logger) Warnf(msg string, fields map[string]any)  { l.Log(LevelWarn, msg, fields) }
func (l *Logger) Errorf(msg string, fields map[string]any) { l.Log(LevelError, msg, fields) }

package mcpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// LogPrefix marks every line the MCP server writes, so a client's server log
// shows at a glance which process spoke (spec 1: "Logging goes to stderr only,
// prefixed `sonar mcp:`").
const LogPrefix = "sonar mcp: "

// ParseLevel maps the --log-level flag onto a slog level. "off" silences the
// server completely.
func ParseLevel(s string) (slog.Level, bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, true, nil
	case "debug":
		return slog.LevelDebug, true, nil
	case "warn", "warning":
		return slog.LevelWarn, true, nil
	case "error":
		return slog.LevelError, true, nil
	case "off", "none", "silent":
		return slog.LevelError, false, nil
	default:
		return 0, false, fmt.Errorf("unknown log level %q: use debug, info, warn, error or off", s)
	}
}

// NewLogger returns a logger writing one prefixed line per record to w, which
// must never be stdout: stdout carries the MCP frames.
func NewLogger(w io.Writer, level slog.Level, enabled bool) *slog.Logger {
	if !enabled || w == nil {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(&prefixHandler{w: w, level: level, mu: &sync.Mutex{}})
}

// prefixHandler writes "sonar mcp: <level> <message> key=value" lines. It is a
// handler rather than a TextHandler wrapper because the prefix has to lead the
// line for a human reading a client's server log, and slog has no way to put
// it there.
type prefixHandler struct {
	w     io.Writer
	level slog.Level
	attrs []slog.Attr
	group string
	mu    *sync.Mutex
}

func (h *prefixHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *prefixHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(LogPrefix)
	b.WriteString(strings.ToLower(r.Level.String()))
	b.WriteByte(' ')
	b.WriteString(r.Message)
	for _, a := range h.attrs {
		writeAttr(&b, h.group, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, h.group, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *prefixHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := *h
	out.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &out
}

func (h *prefixHandler) WithGroup(name string) slog.Handler {
	out := *h
	if name != "" {
		out.group = strings.TrimPrefix(h.group+"."+name, ".")
	}
	return &out
}

func writeAttr(b *strings.Builder, group string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if group != "" {
		key = group + "." + key
	}
	fmt.Fprintf(b, " %s=%v", key, a.Value.Resolve().Any())
}

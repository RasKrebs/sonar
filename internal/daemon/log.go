package daemon

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Log rotation policy from the spec's "Error handling" section: 5 MiB per file,
// three generations kept (daemon.log, daemon.log.1, daemon.log.2).
const (
	LogMaxBytes = 5 << 20
	LogKeep     = 3
)

// RotatingWriter appends to a file and rotates it once it passes MaxBytes,
// keeping Keep generations. It is safe for concurrent writers.
type RotatingWriter struct {
	Path     string
	MaxBytes int64
	Keep     int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// NewRotatingWriter opens path for appending, creating its directory.
func NewRotatingWriter(path string, maxBytes int64, keep int) (*RotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}
	w := &RotatingWriter{Path: path, MaxBytes: maxBytes, Keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(w.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, info.Size()
	return nil
}

// Write appends p, rotating first when the file would grow past MaxBytes.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if w.MaxBytes > 0 && w.size+int64(len(p)) > w.MaxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate shifts daemon.log.N-1 → daemon.log.N and starts a fresh daemon.log.
// The caller holds the mutex.
func (w *RotatingWriter) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	for i := w.Keep - 1; i >= 1; i-- {
		from := w.Path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", w.Path, i-1)
		}
		to := fmt.Sprintf("%s.%d", w.Path, i)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		_ = os.Rename(from, to)
	}
	w.size = 0
	return w.open()
}

// Close closes the underlying file.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// ParseLogLevel maps the config's daemon.log_level to a slog level. Anything
// unrecognised (including the empty string) is info.
func ParseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// NewLogger builds the daemon's logger. It always writes to the rotated log
// file so `sonar daemon log` has something to show, and additionally to stderr
// when the daemon runs in the foreground.
func NewLogger(path string, level slog.Level, alsoStderr bool) (*slog.Logger, io.Closer, error) {
	file, err := NewRotatingWriter(path, LogMaxBytes, LogKeep)
	if err != nil {
		return nil, nil, err
	}
	var w io.Writer = file
	if alsoStderr {
		w = io.MultiWriter(file, os.Stderr)
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(h), file, nil
}

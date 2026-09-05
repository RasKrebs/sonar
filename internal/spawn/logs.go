package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Log rotation policy for detached runs: 10 MiB per file, three generations
// (`<name>.log`, `<name>.log.1`, `<name>.log.2`).
const (
	LogMaxBytes int64 = 10 << 20
	LogKeep           = 3
)

// LogDir is ~/.config/sonar/logs. It mirrors internal/config's layout without
// importing it, the same way internal/runs does.
func LogDir() string {
	if override := os.Getenv("SONAR_LOG_DIR"); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "sonar", "logs")
}

// LogPath is where a detached run's stdout and stderr land:
// ~/.config/sonar/logs/<group>/<name>.log.
func LogPath(group, name string) string {
	if group == "" {
		group = "ungrouped"
	}
	if name == "" {
		name = "run"
	}
	return filepath.Join(LogDir(), sanitizeSegment(group), sanitizeSegment(name)+".log")
}

// OpenLog opens a run's log file for appending, rotating it first when it has
// reached LogMaxBytes. A detached child holds the file descriptor for its whole
// life, so rotation happens when the log is opened rather than mid-write:
// renaming underneath a running writer would only make it write to a file
// nobody can find.
func OpenLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating the log directory: %w", err)
	}
	if err := Rotate(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return f, nil
}

// Rotate shifts <path> to <path>.1 (and .1 to .2, dropping the oldest) when it
// has reached LogMaxBytes. A missing or small file is left alone.
func Rotate(path string) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() < LogMaxBytes {
		return nil
	}
	for i := LogKeep - 1; i >= 1; i-- {
		older := fmt.Sprintf("%s.%d", path, i)
		if i == LogKeep-1 {
			_ = os.Remove(older)
		}
		from := path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", path, i-1)
		}
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if err := os.Rename(from, older); err != nil {
			return fmt.Errorf("rotating %s: %w", from, err)
		}
	}
	return nil
}

// sanitizeSegment keeps a group or service name usable as one path element.
func sanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	repl := func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			return '-'
		}
		if r < 0x20 {
			return '-'
		}
		return r
	}
	s = strings.Map(repl, s)
	s = strings.Trim(s, ". ")
	if s == "" {
		return "run"
	}
	return s
}

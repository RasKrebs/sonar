package ports

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// parseDarwinStats parses: %cpu rss state lstart...
// lstart format: "Mon Jan  2 15:04:05 2006" (5 fields)
func parseDarwinStats(p *ListeningPort, fields []string) {
	if len(fields) < 8 {
		return
	}

	if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
		p.CPUPercent = v
	}
	if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
		p.MemoryRSS = v * 1024 // ps reports RSS in KB
	}
	p.State = decodeState(fields[2])

	// lstart is the remaining fields (e.g. "Wed Mar 18 10:30:00 2026")
	lstart := strings.Join(fields[3:], " ")
	p.StartTime = lstart
	p.Uptime = computeUptime(lstart)
	if p.StartedAt == "" {
		p.StartedAt = parseStartTime(lstart)
	}
	// Thread count is not in this call: macOS needs one `ps -M` per pid, so
	// the caller decides whether that fork is worth paying for (the scan tick
	// pays it, the 1 s stats tick does not).
}

// parseLinuxStats parses: %cpu rss nlwp state lstart...
func parseLinuxStats(p *ListeningPort, fields []string) {
	if len(fields) < 9 {
		return
	}

	if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
		p.CPUPercent = v
	}
	if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
		p.MemoryRSS = v * 1024
	}
	if v, err := strconv.Atoi(fields[2]); err == nil {
		p.ThreadCount = v
	}
	p.State = decodeState(fields[3])

	lstart := strings.Join(fields[4:], " ")
	p.StartTime = lstart
	p.Uptime = computeUptime(lstart)
	if p.StartedAt == "" {
		p.StartedAt = parseStartTime(lstart)
	}
}

// parseWindowsStats parses CSV fields from PowerShell Get-Process:
// fields[0]=CPU, fields[1]=WorkingSet64, fields[2]=ThreadCount, fields[3]=StartTime
func parseWindowsStats(p *ListeningPort, fields []string) {
	if len(fields) < 4 {
		return
	}

	if v, err := strconv.ParseFloat(strings.TrimSpace(fields[0]), 64); err == nil {
		p.CPUPercent = v
	}
	if v, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64); err == nil {
		p.MemoryRSS = v // WorkingSet64 is already in bytes
	}
	if v, err := strconv.Atoi(strings.TrimSpace(fields[2])); err == nil {
		p.ThreadCount = v
	}
	p.State = "running"

	startStr := strings.TrimSpace(fields[3])
	if startStr != "" {
		p.StartTime = startStr
		// StartTime is formatted as ISO 8601 via .ToString('o') in the PowerShell command
		p.Uptime = computeUptime(startStr)
		if p.StartedAt == "" {
			p.StartedAt = parseStartTime(startStr)
		}
	}
}

// countThreadsDarwin counts each pid's threads from a single `ps -M`.
//
// macOS has no batched thread-count column (`nlwp` is a Linux ps extension),
// so this used to be one fork per pid — eighteen of them on the machine this
// was measured on, inside the lock that serializes a scan against the stats
// tick. `ps -M` accepts a pid list and prints one line per process followed by
// one per thread, which is all the counting needs.
func countThreadsDarwin(pids []int) map[int]int {
	counts := map[int]int{}
	if len(pids) == 0 {
		return counts
	}
	want := make(map[int]bool, len(pids))
	pidStrs := make([]string, 0, len(pids))
	for _, pid := range pids {
		if pid <= 0 || want[pid] {
			continue
		}
		want[pid] = true
		pidStrs = append(pidStrs, strconv.Itoa(pid))
	}
	out, err := exec.Command("ps", "-M", "-p", strings.Join(pidStrs, ",")).Output()
	if err != nil {
		return counts
	}
	return countPSThreads(string(out), want)
}

// countPSThreads counts the `ps -M` lines belonging to each requested pid.
//
// A process line is "USER PID TT %CPU ..." and a thread line is "PID %CPU ..."
// with the user and tty columns blank, so the pid is in the first or the
// second field depending on which it is. Both count, which is what the
// single-pid version reported (every line but the header).
func countPSThreads(out string, want map[int]bool) map[int]int {
	counts := map[int]int{}
	current := 0
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if i == 0 && strings.EqualFold(fields[0], "USER") {
			continue // header
		}
		switch {
		case len(fields) > 1 && inSet(want, fields[1]):
			current, _ = strconv.Atoi(fields[1])
		case inSet(want, fields[0]):
			current, _ = strconv.Atoi(fields[0])
		case current == 0:
			continue
		}
		counts[current]++
	}
	return counts
}

// inSet reports whether a field is one of the pids that were asked for.
func inSet(want map[int]bool, field string) bool {
	v, err := strconv.Atoi(field)
	return err == nil && want[v]
}

// decodeState converts the single-char process state to a human-readable string.
func decodeState(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case 'R':
		return "running"
	case 'S':
		return "sleeping"
	case 'D':
		return "disk sleep"
	case 'T':
		return "stopped"
	case 'Z':
		return "zombie"
	case 'I':
		return "idle"
	case 'U':
		return "uninterruptible"
	default:
		return strings.ToLower(s)
	}
}

// startTimeLayouts are the shapes a process start time arrives in: `ps -o
// lstart` (with one or two spaces before a single-digit day) and the ISO-8601
// instant PowerShell reports on Windows.
var startTimeLayouts = []string{
	"Mon Jan  2 15:04:05 2006",
	"Mon Jan 2 15:04:05 2006",
	time.RFC3339Nano,
	time.RFC3339,
}

// startTime parses a raw process start time. `ps` prints local time with no
// zone, so the zoneless layouts have to be read in the local zone: parsing them
// as UTC puts the instant a whole UTC offset away from the truth, which is how
// `uptime` came back as "-7185s" for a process that had been up for seconds
// while `started_at` — which already parsed in the local zone — was right.
//
// One parser now serves both fields, so they cannot disagree again.
func startTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range startTimeLayouts {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseStartTime converts a raw ps lstart string (or the ISO-8601 timestamp
// PowerShell reports on Windows) to RFC3339. Returns "" when it cannot be
// parsed. `started_at` in the contract is RFC3339.
func parseStartTime(raw string) string {
	t, ok := startTime(raw)
	if !ok {
		return ""
	}
	return t.Format(time.RFC3339)
}

// computeUptime is how long ago a process started, rendered for a human.
func computeUptime(lstart string) string { return computeUptimeAt(lstart, time.Now()) }

// computeUptimeAt is computeUptime against an explicit clock, so the conversion
// can be tested without racing real time.
func computeUptimeAt(lstart string, now time.Time) string {
	t, ok := startTime(lstart)
	if !ok {
		return ""
	}
	return formatDuration(now.Sub(t))
}

// formatDuration returns a concise human-readable duration string. A negative
// duration — a clock that stepped back, or a start time a moment in the future
// — reads as 0s rather than as a nonsense negative uptime.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	return fmt.Sprintf("%dd%dh", days, h)
}

// FormatBytes returns a human-readable byte size.
func FormatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

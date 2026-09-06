package mcpserver

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/raskrebs/sonar/internal/state"
)

// MaxRows caps the text block of a list result (spec 1, "Concurrency and
// timeouts"). Structured content is never truncated: the cap exists so a
// hundred containers do not eat the model's context, not to hide data.
const MaxRows = 200

// truncationNote is the sentence appended when rows were dropped from the text.
func truncationNote(shown, total int) string {
	return fmt.Sprintf("showing %d of %d, filter with group/session/type", shown, total)
}

// renderPorts is the text block of every list-of-ports tool: a header line
// saying how many, then a compact fixed-width table.
func renderPorts(ports []state.Port) string {
	if len(ports) == 0 {
		return "no matching ports are listening"
	}

	shown := ports
	truncated := false
	if len(shown) > MaxRows {
		shown, truncated = shown[:MaxRows], true
	}

	withSession := false
	for _, p := range shown {
		if p.Session != nil && p.Session.ID != "" {
			withSession = true
			break
		}
	}

	header := []string{"PORT", "PID", "NAME", "TYPE", "GROUP", "HEALTH", "URL"}
	if withSession {
		header = []string{"PORT", "PID", "NAME", "TYPE", "GROUP", "SESSION", "HEALTH", "URL"}
	}

	rows := make([][]string, 0, len(shown))
	for _, p := range shown {
		row := []string{
			strconv.Itoa(p.Port),
			strconv.Itoa(p.PID),
			dash(p.DisplayName),
			string(p.Type),
			dash(deref(p.Group)),
		}
		if withSession {
			row = append(row, dash(sessionLabel(p)))
		}
		row = append(row, dash(healthLabel(p)), dash(p.URL))
		rows = append(rows, row)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d listening %s\n\n", len(ports), plural(len(ports), "port", "ports"))
	b.WriteString(table(header, rows))
	if truncated {
		b.WriteString("\n" + truncationNote(len(shown), len(ports)))
	}
	return b.String()
}

// renderPort is the text block of a single-object tool: a key/value block,
// omitting what this port does not have rather than printing a wall of nulls.
func renderPort(p state.Port) string {
	kv := [][2]string{
		{"port", strconv.Itoa(p.Port)},
		{"bind_address", p.BindAddress},
		{"url", p.URL},
		{"pid", strconv.Itoa(p.PID)},
		{"process", p.Process},
		{"display_name", p.DisplayName},
		{"type", string(p.Type)},
		{"user", p.User},
	}
	kv = appendIf(kv, "command", p.Command)
	kv = appendIf(kv, "cwd", p.Cwd)
	kv = appendIf(kv, "project_root", deref(p.ProjectRoot))
	kv = appendIf(kv, "group", deref(p.Group))
	if p.GroupSource != nil {
		kv = appendIf(kv, "group_source", string(*p.GroupSource))
	}
	if p.Run != nil {
		kv = appendIf(kv, "run", fmt.Sprintf("%s (name %s, root pid %d)", p.Run.ID, dash(p.Run.Name), p.Run.RootPID))
	}
	if p.Session != nil && p.Session.ID != "" {
		kv = appendIf(kv, "session", sessionLine(*p.Session))
	}
	if p.Docker != nil {
		kv = appendIf(kv, "docker", fmt.Sprintf("container %s, image %s, compose %s/%s",
			p.Docker.Container, p.Docker.Image, p.Docker.ComposeProject, p.Docker.ComposeService))
	}
	if p.Health != nil {
		kv = appendIf(kv, "health", healthLine(*p.Health))
	}
	if p.Stats != nil {
		kv = appendIf(kv, "stats", fmt.Sprintf("cpu %.1f%%, rss %s, threads %d, uptime %s",
			p.Stats.CPUPercent, bytesHuman(p.Stats.MemoryRSS), p.Stats.ThreadCount, dash(p.Stats.Uptime)))
	}
	kv = appendIf(kv, "started_at", deref(p.StartedAt))
	kv = appendIf(kv, "service_unit", deref(p.ServiceUnit))
	if len(p.ExposedURLs) > 0 {
		kv = appendIf(kv, "exposed_urls", strings.Join(p.ExposedURLs, ", "))
	}

	width := 0
	for _, pair := range kv {
		width = max(width, len(pair[0]))
	}
	var b strings.Builder
	for _, pair := range kv {
		fmt.Fprintf(&b, "%-*s  %s\n", width, pair[0], pair[1])
	}
	return strings.TrimRight(b.String(), "\n")
}

// table lays out fixed-width columns, sized to their contents. The last column
// is not padded, so a long URL never trails whitespace.
func table(header []string, rows [][]string) string {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], len(cell))
		}
	}

	var b strings.Builder
	writeRow := func(cells []string) {
		for i, cell := range cells {
			if i == len(cells)-1 {
				b.WriteString(cell)
				continue
			}
			fmt.Fprintf(&b, "%-*s  ", widths[i], cell)
		}
		b.WriteByte('\n')
	}
	writeRow(header)
	for _, row := range rows {
		writeRow(row)
	}
	return strings.TrimRight(b.String(), "\n")
}

func sessionLabel(p state.Port) string {
	if p.Session == nil {
		return ""
	}
	if p.Session.Label != "" {
		return p.Session.Label
	}
	return p.Session.ID
}

func sessionLine(s state.Session) string {
	parts := []string{s.ID}
	if s.Tool != "" {
		parts = append(parts, "tool "+s.Tool)
	}
	if s.Worktree != "" {
		parts = append(parts, "worktree "+s.Worktree)
	}
	if s.Branch != "" {
		parts = append(parts, "branch "+s.Branch)
	}
	if s.Detected {
		parts = append(parts, "detected")
	}
	return strings.Join(parts, ", ")
}

func healthLabel(p state.Port) string {
	if p.Health == nil {
		return ""
	}
	return p.Health.Status
}

func healthLine(h state.Health) string {
	out := h.Status
	if h.Reason != "" && h.Reason != h.Status {
		out += " (" + h.Reason + ")"
	}
	if h.Code != 0 {
		out += fmt.Sprintf(", http %d", h.Code)
	}
	if h.LatencyMs != 0 {
		out += fmt.Sprintf(", %dms", h.LatencyMs)
	}
	return out
}

func bytesHuman(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGT"[exp])
}

func appendIf(kv [][2]string, key, value string) [][2]string {
	if value == "" {
		return kv
	}
	return append(kv, [2]string{key, value})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

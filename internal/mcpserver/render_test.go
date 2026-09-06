package mcpserver_test

import (
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/mcpserver"
	"github.com/raskrebs/sonar/internal/mcpserver/fakedaemon"
	"github.com/raskrebs/sonar/internal/state"
)

// TestListPortsTextIsCappedAt200Rows: the text block is what lands in the
// model's context, so it is capped; the structured content, which a client
// reads programmatically, is not (spec 1).
func TestListPortsTextIsCappedAt200Rows(t *testing.T) {
	const total = 250

	fx := fakedaemon.DefaultFixture()
	fx.Ports = fakedaemon.ManyPorts(total)
	h := newHarnessWith(t, fx)

	res := h.call("list_ports", map[string]any{})

	out := structured[struct {
		Ports []state.Port `json:"ports"`
	}](t, res)
	if len(out.Ports) != total {
		t.Fatalf("structured content carried %d ports, want all %d", len(out.Ports), total)
	}

	text := textOf(res)
	if !strings.Contains(text, "showing 200 of 250") {
		t.Errorf("the text does not say it was truncated:\n%s", firstLines(text, 4))
	}
	if !strings.Contains(text, "filter with group/session/type") {
		t.Errorf("the truncation note does not say how to narrow the list:\n%s", lastLine(text))
	}

	// One header line, 200 rows, a blank line, a count line and the note.
	rows := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "4") { // the synthetic ports are 4000+
			rows++
		}
	}
	if rows != mcpserver.MaxRows {
		t.Errorf("the table has %d rows, want %d", rows, mcpserver.MaxRows)
	}
}

func TestListPortsTextIsATable(t *testing.T) {
	h := newHarness(t)

	text := textOf(h.call("list_ports", map[string]any{}))
	lines := strings.Split(text, "\n")
	if len(lines) < 3 {
		t.Fatalf("the text block is not a table:\n%s", text)
	}
	if !strings.HasPrefix(lines[0], "5 listening ports") {
		t.Errorf("the first line should count the ports, got %q", lines[0])
	}
	header := lines[2]
	for _, col := range []string{"PORT", "PID", "NAME", "TYPE", "GROUP", "SESSION", "HEALTH", "URL"} {
		if !strings.Contains(header, col) {
			t.Errorf("the header is missing %s: %q", col, header)
		}
	}
	if !strings.Contains(text, "3000") || !strings.Contains(text, "shop") {
		t.Errorf("the table does not carry the fixture's rows:\n%s", text)
	}
}

func TestEmptyListSaysSo(t *testing.T) {
	fx := fakedaemon.DefaultFixture()
	fx.Ports = nil
	h := newHarnessWith(t, fx)

	if got := textOf(h.call("list_ports", map[string]any{})); !strings.Contains(got, "no matching ports") {
		t.Errorf("an empty list should say so, got %q", got)
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

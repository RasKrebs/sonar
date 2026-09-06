package mcpserver_test

import (
	"io"
	"os"
	"strings"
	"testing"
)

// TestNothingIsWrittenToStdout guards the one rule a stdio MCP server cannot
// break: stdout carries the protocol frames, so a stray Println anywhere under
// mcpserver corrupts the session. The whole surface — startup, a tool call, an
// argument error, a daemon that dies and comes back — runs with os.Stdout
// replaced by a pipe, which must stay empty.
func TestNothingIsWrittenToStdout(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	real := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = real }()

	captured := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		captured <- b
	}()

	func() {
		h := newHarness(t)
		h.call("list_ports", map[string]any{})
		h.call("list_ports", map[string]any{"type": "nonsense"})
		h.call("inspect_port", map[string]any{"port": 3000})
		h.call("inspect_port", map[string]any{"port": 9999})
		if _, err := h.client.ListTools(t.Context(), nil); err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		h.fake.Stop()
		h.call("list_ports", map[string]any{})

		// Logging is on at debug level throughout, and none of it may land here.
		if !strings.Contains(h.logs.String(), "sonar mcp: ") {
			t.Errorf("the logger wrote nothing, so this test proved nothing:\n%s", h.logs.String())
		}
	}()

	os.Stdout = real
	_ = w.Close()
	if got := <-captured; len(got) > 0 {
		t.Fatalf("mcpserver wrote %d bytes to stdout, which is the MCP transport:\n%s", len(got), got)
	}
}

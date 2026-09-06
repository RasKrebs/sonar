//go:build integration

// End-to-end tests for `sonar mcp`, run with
// `go test -tags integration ./internal/mcpserver/...`. They build the real
// binary, drive it over stdio the way Claude Code does — an SDK
// CommandTransport, the same framing — and let it autostart a real daemon in a
// temp HOME on a temp socket.
//
// This is also step 2A.1's acceptance demo in executable form: `tools/list`
// shows the tools, and `list_ports` returns the same rows as
// `sonar list --json`.
package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/raskrebs/sonar/internal/state"
)

func TestRealDaemonToolsList(t *testing.T) {
	e := newRealEnv(t)
	session := e.connect(t)

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := []string{}
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		t.Logf("%-13s readOnly=%v openWorld=%v", tool.Name,
			tool.Annotations.ReadOnlyHint, *tool.Annotations.OpenWorldHint)
	}
	if len(names) != 2 || names[0] != "inspect_port" && names[0] != "list_ports" {
		t.Fatalf("tools/list = %v, want list_ports and inspect_port", names)
	}
}

// TestListPortsEqualsSonarListJSON is the step's demo: what the tool returns
// and what the CLI prints are the same rows, because they are the same daemon
// call. The machine underneath is live, so a row can appear or vanish between
// the two reads; the comparison is retried rather than made lenient.
func TestListPortsEqualsSonarListJSON(t *testing.T) {
	e := newRealEnv(t)
	port := e.listen(t) // a port of our own, so the list is never empty
	session := e.connect(t)

	var lastDiff string
	for attempt := range 5 {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "list_ports",
			Arguments: map[string]any{"include_apps": true},
		})
		if err != nil {
			t.Fatalf("list_ports: %v", err)
		}
		if res.IsError {
			t.Fatalf("list_ports failed: %s", textOf(res))
		}
		var tool struct {
			Ports []state.Port `json:"ports"`
		}
		decodeStructured(t, res, &tool)

		cli := e.listJSON(t)

		if !hasPort(tool.Ports, port) {
			t.Fatalf("list_ports did not report the test's own listener on %d", port)
		}
		toolJSON := mustJSON(t, tool.Ports)
		cliJSON := mustJSON(t, cli)
		if string(toolJSON) == string(cliJSON) {
			t.Logf("list_ports matched `sonar list --json --all` on attempt %d (%d ports)",
				attempt+1, len(cli))
			return
		}
		lastDiff = firstDifference(string(toolJSON), string(cliJSON))
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("list_ports never matched `sonar list --json --all`:\n%s", lastDiff)
}

// TestInspectPortNotFoundOverStdio checks the error model end to end: a domain
// failure is a result the client can read, not a transport error.
func TestInspectPortNotFoundOverStdio(t *testing.T) {
	e := newRealEnv(t)
	session := e.connect(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "inspect_port",
		Arguments: map[string]any{"port": 65535},
	})
	if err != nil {
		t.Fatalf("the call itself must not fail: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected an error result, got %s", textOf(res))
	}
	if !strings.HasPrefix(textOf(res), "not_found: ") {
		t.Errorf("text = %q, want it to start with not_found:", textOf(res))
	}
	t.Logf("inspect_port 65535 →\n%s", textOf(res))
}

// TestStderrCarriesTheLogs is the other half of stdout hygiene: the server
// talks on stderr, and the session initialised at all, which it could not have
// done if anything else had landed on stdout.
func TestStderrCarriesTheLogs(t *testing.T) {
	e := newRealEnv(t)
	session := e.connect(t)
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	_ = session.Close()

	stderr := e.stderr()
	if !strings.Contains(stderr, "sonar mcp: ") {
		t.Errorf("stderr does not carry the server's prefixed log lines:\n%s", stderr)
	}
}

// TestSocketFlagOverridesTheEnvironment: `--socket` is what a client config
// uses to point one agent at a daemon other than the default.
func TestSocketFlagOverridesTheEnvironment(t *testing.T) {
	e := newRealEnv(t)

	cmd := exec.Command(e.bin, "mcp", "--socket", e.socket, "--log-level", "debug")
	// SONAR_SOCKET is deliberately absent: only the flag says where to dial.
	cmd.Env = append(os.Environ(),
		"HOME="+e.home, "USERPROFILE="+e.home, "XDG_RUNTIME_DIR=", "SONAR_NO_HINTS=1")
	cmd.Stderr = &stderrSink{e: e}

	client := mcp.NewClient(&mcp.Implementation{Name: "sonar-itest", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connecting with --socket: %v\nstderr:\n%s", err, e.stderr())
	}
	defer session.Close()

	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if !strings.Contains(e.stderr(), e.socket) {
		t.Errorf("the server did not report the socket it was pointed at:\n%s", e.stderr())
	}
}

// ------------------------------------------------------------------ setup ---

type realEnv struct {
	bin    string
	home   string
	socket string

	mu  sync.Mutex
	err strings.Builder
}

func newRealEnv(t *testing.T) *realEnv {
	t.Helper()
	bin, err := buildSonar()
	if err != nil {
		t.Fatal(err)
	}

	// macOS caps a unix socket path at ~104 bytes and t.TempDir() is long.
	sockDir, err := os.MkdirTemp("", "snrmcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })

	socket := filepath.Join(sockDir, "d.sock")
	if runtime.GOOS == "windows" {
		socket = fmt.Sprintf(`\\.\pipe\sonar-mcp-itest-%d-%d`, os.Getpid(), pipeSeq.Add(1))
	}

	e := &realEnv{bin: bin, home: t.TempDir(), socket: socket}
	t.Cleanup(func() { e.run(t, "daemon", "stop") })
	return e
}

func (e *realEnv) env() []string {
	return append(os.Environ(),
		"HOME="+e.home,
		"USERPROFILE="+e.home,
		"XDG_RUNTIME_DIR=",
		"SONAR_SOCKET="+e.socket,
		"SONAR_NO_HINTS=1",
	)
}

// connect starts `sonar mcp` and speaks MCP to it over its stdio.
func (e *realEnv) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	cmd := exec.Command(e.bin, "mcp", "--log-level", "debug")
	cmd.Env = e.env()
	cmd.Stderr = &stderrSink{e: e}

	client := mcp.NewClient(&mcp.Implementation{Name: "sonar-itest", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connecting to `sonar mcp`: %v\nstderr:\n%s", err, e.stderr())
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// listJSON runs `sonar list --json --all` against the same daemon.
func (e *realEnv) listJSON(t *testing.T) []state.Port {
	t.Helper()
	out := e.run(t, "list", "--json", "--all")
	var rows []state.Port
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("decoding `sonar list --json`: %v\n%s", err, out)
	}
	return rows
}

func (e *realEnv) run(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Env = e.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("`sonar %s` failed: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// listen opens a listening socket owned by this test process, so the port list
// is never empty and always has one row both readers must agree on.
func (e *realEnv) listen(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func (e *realEnv) stderr() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err.String()
}

type stderrSink struct{ e *realEnv }

func (s *stderrSink) Write(p []byte) (int, error) {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	return s.e.err.Write(p)
}

var pipeSeq atomic.Int64

var buildSonar = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "sonar-mcp-itest-bin")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "sonar")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = moduleRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building sonar: %v: %s", err, out)
	}
	return bin, nil
})

func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return "."
}

func decodeStructured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decoding structured content: %v\n%s", err, raw)
	}
}

func hasPort(ports []state.Port, want int) bool {
	for _, p := range ports {
		if p.Port == want {
			return true
		}
	}
	return false
}

// firstDifference points at the byte where two JSON documents diverge, which is
// more use than printing two multi-kilobyte blobs.
func firstDifference(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(i-120, 0)
	return fmt.Sprintf("diverge at byte %d:\n  tool: %s\n   cli: %s",
		i, clip(a[from:], 260), clip(b[from:], 260))
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

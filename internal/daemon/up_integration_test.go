//go:build integration

// Spec integration test 4, in the shape step 1A.7 gives it: `sonar up` starts a
// three-service group in depends_on order, `sonar kill -g` stops all three, and
// the daemon's history has the rows to show for it. Run with
// `go test -tags integration ./internal/daemon/...`.
package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/groups"
)

// TestUpStartsAGroupInOrderAndKillStopsIt is the step's acceptance path end to
// end, through the real binary: a config with db, api and frontend, one of them
// waiting on another, started detached and then killed as a group.
func TestUpStartsAGroupInOrderAndKillStopsIt(t *testing.T) {
	listener, buildErr := buildListener()
	if buildErr != nil {
		t.Fatal(buildErr)
	}

	e := newEnv(t)
	// The project lives inside the temp HOME: the daemon refuses to start a
	// command outside the user's home unless it is asked to.
	project := filepath.Join(e.home, "demo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	dbPort, apiPort, webPort := unusedPort(t), unusedPort(t), unusedPort(t)
	config := fmt.Sprintf(`# the demo project
name: demo
services:
  - name: db
    cmd: '%s %d'
    port: %d
  - name: api
    cmd: '%s %d'
    port: %d
    depends_on: [db]
  - name: frontend
    cmd: '%s %d'
    port: %d
`, listener, dbPort, dbPort, listener, apiPort, apiPort, listener, webPort, webPort)
	configPath := filepath.Join(project, groups.ConfigName)
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	e.serve()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// A subscriber keeps the daemon's scan loop running: with nobody
	// listening it parks, and the history ring is fed from published deltas.
	c := e.connect(ctx)
	sub, err := c.Subscribe(ctx, client.SubscribeOptions{Events: true, Buffer: 512})
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	// `sonar up` from inside the project: no argument, so the group comes from
	// the cwd's .sonar.yaml.
	up := e.command("up", "--json")
	up.Dir = project
	upOut, err := up.CombinedOutput()
	if err != nil {
		t.Fatalf("sonar up: %v\n%s", err, upOut)
	}
	t.Logf("sonar up --json:\n%s", upOut)

	var summary struct {
		Services []rpc.GroupsStartChunk `json:"services"`
		Started  []string               `json:"started"`
		Skipped  []string               `json:"skipped"`
		Errors   []string               `json:"errors"`
	}
	if err := json.Unmarshal(upOut, &summary); err != nil {
		t.Fatalf("decoding sonar up --json: %v\n%s", err, upOut)
	}
	// Registered before the first assertion: a failure half way through must
	// not leave three detached listeners squatting on ports for the next run.
	t.Cleanup(func() { killChunks(summary.Services) })

	if len(summary.Errors) != 0 {
		t.Fatalf("sonar up reported failures: %+v", summary)
	}
	if len(summary.Started) != 3 {
		t.Fatalf("started = %v, want all three services", summary.Started)
	}
	if summary.Services[0].Service != "db" {
		t.Errorf("the first service started was %q, want db (nothing depends_on it)",
			summary.Services[0].Service)
	}
	var apiChunk rpc.GroupsStartChunk
	for _, c := range summary.Services {
		if c.Service == "api" {
			apiChunk = c
		}
		if c.PID <= 0 || c.LogPath == "" {
			t.Errorf("chunk %+v carries no pid or log path", c)
		}
		if !strings.Contains(c.LogPath, filepath.Join("logs", "demo", c.Service+".log")) {
			t.Errorf("log path %q is not ~/.config/sonar/logs/demo/%s.log", c.LogPath, c.Service)
		}
	}
	if apiChunk.PID == 0 {
		t.Fatal("api never started")
	}

	// api only started once db was listening, so by now all three are up and
	// the group is running.
	waitForGroupStatus(t, e, project, "demo", "running", 45*time.Second)

	tree := e.command("list", "--tree")
	tree.Dir = project
	treeOut, err := tree.CombinedOutput()
	if err != nil {
		t.Fatalf("sonar list --tree: %v\n%s", err, treeOut)
	}
	t.Logf("sonar list --tree:\n%s", treeOut)
	if !strings.Contains(string(treeOut), "demo") {
		t.Errorf("the group is not in the tree:\n%s", treeOut)
	}

	// Running `up` again is a no-op: everything is already running.
	again := e.command("up", "--json")
	again.Dir = project
	againOut, err := again.CombinedOutput()
	if err != nil {
		t.Fatalf("second sonar up: %v\n%s", err, againOut)
	}
	var second struct {
		Skipped []string `json:"skipped"`
		Started []string `json:"started"`
	}
	if err := json.Unmarshal(againOut, &second); err != nil {
		t.Fatalf("decoding the second sonar up: %v\n%s", err, againOut)
	}
	if len(second.Started) != 0 || len(second.Skipped) != 3 {
		t.Errorf("a second `sonar up` = started %v, skipped %v; want everything skipped",
			second.Started, second.Skipped)
	}

	// `sonar kill -g demo` stops all three.
	kill := e.command("kill", "-g", "demo", "--yes")
	kill.Dir = project
	killOut, err := kill.CombinedOutput()
	if err != nil {
		t.Fatalf("sonar kill -g demo: %v\n%s", err, killOut)
	}
	t.Logf("sonar kill -g demo:\n%s", killOut)

	for _, port := range []int{dbPort, apiPort, webPort} {
		if !waitForPortGone(t, port, 45*time.Second) {
			t.Errorf("port %d is still listening after `sonar kill -g demo`", port)
		}
	}

	// The history ring is written from published deltas, so wait for the
	// daemon to publish the three port_down events before reading it back.
	waitForDownEvents(t, sub, []int{dbPort, apiPort, webPort}, 45*time.Second)

	// The daemon wrote the transitions to the history ring.
	history := e.command("history", "--json", "--limit", "100")
	history.Dir = project
	histOut, err := history.CombinedOutput()
	if err != nil {
		t.Fatalf("sonar history: %v\n%s", err, histOut)
	}
	var events []rpc.HistoryEvent
	if err := json.Unmarshal(histOut, &events); err != nil {
		t.Fatalf("decoding sonar history --json: %v\n%s", err, histOut)
	}
	t.Logf("sonar history --json: %d rows", len(events))

	wantUp := map[int]bool{dbPort: false, apiPort: false, webPort: false}
	wantDown := map[int]bool{dbPort: false, apiPort: false, webPort: false}
	for _, ev := range events {
		switch ev.Kind {
		case "port_up", "port_restarted":
			if _, ok := wantUp[ev.Port]; ok {
				wantUp[ev.Port] = true
			}
		case "port_down":
			if _, ok := wantDown[ev.Port]; ok {
				wantDown[ev.Port] = true
			}
		}
	}
	for port, seen := range wantUp {
		if !seen {
			t.Errorf("no port_up row for %d in the history", port)
		}
	}
	for port, seen := range wantDown {
		if !seen {
			t.Errorf("no port_down row for %d in the history", port)
		}
	}
}

// waitForDownEvents drains the subscription until every port has been reported
// as gone.
func waitForDownEvents(t *testing.T, sub *client.Subscription, want []int, timeout time.Duration) {
	t.Helper()
	pending := map[int]bool{}
	for _, p := range want {
		pending[p] = true
	}
	deadline := time.After(timeout)
	for len(pending) > 0 {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				t.Fatal("the subscription closed before the ports went down")
			}
			if ev.Kind == "port_down" && ev.Port != nil {
				delete(pending, ev.Port.Port)
			}
		case <-sub.Deltas:
			// Deltas have to be drained or the subscription is dropped.
		case <-deadline:
			t.Fatalf("no port_down event for %v within %s", keys(pending), timeout)
		}
	}
}

func keys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// waitForGroupStatus polls `sonar groups --json` from inside the project until
// the named group reaches the wanted status.
//
// It runs in the project directory on purpose: `sonar groups` is a direct-scan
// command, and the only directories it indexes are the caller's own and the
// working directories of the processes it scanned. The second half does not
// exist on Windows — there is no per-process cwd the scanner can read — so
// asking from somewhere else is asking a question the command cannot answer
// there.
func waitForGroupStatus(t *testing.T, e *env, dir, name, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last, lastOut string
	var lastErr error
	for time.Now().Before(deadline) {
		cmd := e.command("groups", "--json")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		lastOut, lastErr = string(out), err
		if err == nil {
			var rows []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			}
			if json.Unmarshal(out, &rows) == nil {
				for _, g := range rows {
					if g.Name == name {
						last = g.Status
						if g.Status == want {
							return
						}
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	reportGroupDiagnostics(t, e, dir, lastOut, lastErr)
	t.Fatalf("group %s never reached status %q (last saw %q)", name, want, last)
}

// reportGroupDiagnostics dumps everything that decides a group before the test
// fails on it: what `sonar groups` last said, the runs the daemon registered,
// the mirrored registry the direct scan reads, and the ports with the three
// fields attribution turns on. A group missing on one platform is either a
// missing cwd, a missing run record or a missing config, and this says which.
func reportGroupDiagnostics(t *testing.T, e *env, dir, lastOut string, lastErr error) {
	t.Helper()
	t.Logf("last `sonar groups --json` (err %v):\n%s", lastErr, lastOut)

	for _, args := range [][]string{{"runs", "--json"}, {"list", "--json"}, {"list", "-a", "--json"}} {
		cmd := e.command(args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		t.Logf("`sonar %s` (err %v):\n%s", strings.Join(args, " "), err, out)
	}

	// `list` hides desktop apps and `list -a` does not, so a scan that reaches
	// the daemon and still shows nothing is either an empty scan or a
	// classification that hid it. The raw OS output says which.
	scan := exec.Command("netstat", "-ano")
	if runtime.GOOS != "windows" {
		scan = exec.Command("lsof", "-iTCP", "-sTCP:LISTEN", "-n", "-P")
	}
	out, err := scan.CombinedOutput()
	t.Logf("`%s` (err %v):\n%s", strings.Join(scan.Args, " "), err, out)

	registry := filepath.Join(e.home, ".config", "sonar", "runs.json")
	if data, err := os.ReadFile(registry); err == nil {
		t.Logf("%s:\n%s", registry, data)
	} else {
		t.Logf("%s could not be read: %v", registry, err)
	}
}

// unusedPort is freePort plus the check freePort cannot make: that nothing is
// actually answering there. A run that was killed mid-flight leaves detached
// services behind, and a config pointing at one of their ports would make
// `sonar up` skip everything as "already running".
func unusedPort(t *testing.T) int {
	t.Helper()
	seen := map[int]bool{}
	for i := 0; i < 50; i++ {
		port := freePort(t)
		if seen[port] || portOpen(port) {
			continue
		}
		seen[port] = true
		return port
	}
	t.Fatal("could not find a port nothing is listening on")
	return 0
}

// waitForPortGone reports whether a port stopped listening in time.
func waitForPortGone(t *testing.T, port int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !portOpen(port) {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func portOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// killChunks stops anything a failed run left behind.
func killChunks(chunks []rpc.GroupsStartChunk) {
	for _, c := range chunks {
		if c.PID <= 0 {
			continue
		}
		if p, err := os.FindProcess(c.PID); err == nil {
			_ = p.Kill()
		}
	}
}

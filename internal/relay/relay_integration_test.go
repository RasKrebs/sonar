//go:build integration

// Integration test for `sonar relay serve`, run with
// `go test -tags integration ./internal/relay/...`. It builds the real binary,
// starts the relay on a random port in a temp directory, posts a batch and
// reads it back out of /v1/stats — the whole path a self-hosted deployment
// takes, flags, storage and all.
package relay_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRelayServeEndToEnd(t *testing.T) {
	dir := t.TempDir()
	bin := buildSonar(t, dir)
	dbPath := filepath.Join(dir, "relay.db")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// :0 and the address out of the startup log: no port to guess, so two
	// copies of this test can run side by side.
	cmd := exec.CommandContext(ctx, bin, "relay", "serve",
		"--listen", "127.0.0.1:0",
		"--db", dbPath,
		"--project-keys", "test-key",
		"--retention-days", "7",
	)
	cmd.Env = append(os.Environ(), "HOME="+dir, "USERPROFILE="+dir)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the relay: %v", err)
	}

	var logs strings.Builder
	var mu sync.Mutex
	addrc := make(chan string, 1)
	go func() {
		dec := json.NewDecoder(stderr)
		for {
			var line map[string]any
			if err := dec.Decode(&line); err != nil {
				return
			}
			mu.Lock()
			fmt.Fprintf(&logs, "%v\n", line)
			mu.Unlock()
			if line["msg"] == "relay listening" {
				if a, ok := line["addr"].(string); ok {
					select {
					case addrc <- a:
					default:
					}
				}
			}
		}
	}()

	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	}()

	var addr string
	select {
	case addr = <-addrc:
	case <-time.After(30 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("the relay never logged its address; log so far:\n%s", logs.String())
	}
	base := "http://" + addr

	waitHealthy(t, base)

	// The keys are configured, so an anonymous batch is a 401.
	if code, _ := doPost(t, base+"/v1/events", "", eventsBody); code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated POST = %d, want 401", code)
	}

	code, body := doPost(t, base+"/v1/events", "test-key", eventsBody)
	if code != http.StatusAccepted {
		t.Fatalf("POST /v1/events = %d (%s), want 202", code, body)
	}
	var accepted struct {
		Accepted int `json:"accepted"`
	}
	if err := json.Unmarshal([]byte(body), &accepted); err != nil {
		t.Fatalf("response %q: %v", body, err)
	}
	if accepted.Accepted != 2 {
		t.Fatalf("accepted = %d, want 2", accepted.Accepted)
	}

	// A batch with a path in a prop is refused, with a reason.
	code, body = doPost(t, base+"/v1/events", "test-key",
		`{"install_id":"8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c","events":`+
			`[{"name":"scan","props":{"root":"/Users/ada/code"}}]}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "unsafe_prop") {
		t.Fatalf("a path prop = %d (%s), want 400 unsafe_prop", code, body)
	}

	stats := getStats(t, base, "test-key")
	if stats.Installs.Active1d != 1 || stats.Installs.Total != 1 {
		t.Fatalf("installs = %+v, want one active install", stats.Installs)
	}
	names := map[string]int64{}
	for _, c := range stats.Events {
		names[c.Name] += c.Count
	}
	if names["app_open"] != 1 || names["group_up"] != 1 {
		t.Fatalf("events = %+v, want one app_open and one group_up", stats.Events)
	}

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("the relay did not create --db: %v", err)
	}
	// The relay must not have touched the daemon's config directory.
	if _, err := os.Stat(filepath.Join(dir, ".config", "sonar")); err == nil {
		t.Fatal("the relay wrote into ~/.config/sonar; it is not a sonar install")
	}
}

const eventsBody = `{
  "install_id": "8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c",
  "app_version": "v0.6.0",
  "daemon_version": "v0.6.0",
  "os": "linux",
  "arch": "amd64",
  "events": [
    {"name": "app_open", "props": {"pane": "ports"}},
    {"name": "group_up", "props": {"services": 3}}
  ]
}`

type statsBody struct {
	Days     int `json:"days"`
	Installs struct {
		Active1d  int `json:"active_1d"`
		Active7d  int `json:"active_7d"`
		Active30d int `json:"active_30d"`
		Total     int `json:"total"`
	} `json:"installs"`
	Events []struct {
		Day      string `json:"day"`
		Name     string `json:"name"`
		Count    int64  `json:"count"`
		Installs int64  `json:"installs"`
	} `json:"events"`
}

func getStats(t *testing.T, base, key string) statsBody {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/v1/stats", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Sonar-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/stats: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/stats = %d (%s)", resp.StatusCode, raw)
	}
	var out statsBody
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("stats body %q: %v", raw, err)
	}
	return out
}

func doPost(t *testing.T, url, key, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-Sonar-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func waitHealthy(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the relay never answered /healthz")
}

func buildSonar(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "sonar")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building sonar: %v\n%s", err, out)
	}
	return bin
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// .../internal/relay -> repo root
	return filepath.Dir(filepath.Dir(wd))
}

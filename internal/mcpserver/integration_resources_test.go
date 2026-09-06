//go:build integration

// Step 2A.3's demo in executable form: over stdio against a real daemon,
// subscribe to sonar://ports, start a listener, and watch the resources/updated
// notification arrive; stop the listener and watch the next one; unsubscribe
// and watch the daemon's subscriber count go back to zero.
package mcpserver_test

import (
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/mcpserver"
)

func TestRealDaemonResourcesListAndRead(t *testing.T) {
	e := newRealEnv(t)
	port := e.listen(t)
	session := e.connect(t)

	res, err := session.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	uris := []string{}
	for _, r := range res.Resources {
		uris = append(uris, r.URI)
		t.Logf("%-18s %s", r.URI, r.MIMEType)
	}
	for _, want := range []string{mcpserver.URIPorts, mcpserver.URIGroups, mcpserver.URISessions} {
		if !contains(uris, want) {
			t.Fatalf("resources/list = %v, want it to carry %s", uris, want)
		}
	}

	read, err := session.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: mcpserver.URIPorts})
	if err != nil {
		t.Fatalf("reading %s: %v", mcpserver.URIPorts, err)
	}
	var body rpc.PortsListResult
	if err := json.Unmarshal([]byte(read.Contents[0].Text), &body); err != nil {
		t.Fatalf("decoding %s: %v", mcpserver.URIPorts, err)
	}
	if !hasPort(body.Ports, port) {
		t.Fatalf("sonar://ports does not carry this test's own listener on %d", port)
	}
	t.Logf("sonar://ports carries %d ports, including this test's %d", len(body.Ports), port)
}

// TestRealDaemonResourceSubscription is the demo: a change on the machine
// becomes a resources/updated in the client, and unsubscribing gives the
// daemon its idle timeout back.
func TestRealDaemonResourceSubscription(t *testing.T) {
	e := newRealEnv(t)

	updates := make(chan string, 32)
	cmd := exec.Command(e.bin, "mcp", "--log-level", "debug")
	cmd.Env = e.env()
	cmd.Stderr = &stderrSink{e: e}
	client := mcp.NewClient(&mcp.Implementation{Name: "sonar-itest", Version: "1"},
		&mcp.ClientOptions{
			ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
				updates <- req.Params.URI
			},
		})
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connecting to `sonar mcp`: %v\nstderr:\n%s", err, e.stderr())
	}
	defer session.Close()

	ctx := context.Background()
	if err := session.Subscribe(ctx, &mcp.SubscribeParams{URI: mcpserver.URIPorts}); err != nil {
		t.Fatalf("subscribing to %s: %v", mcpserver.URIPorts, err)
	}
	waitForStatus(t, e, "the daemon to count the MCP server as a subscriber", func(s daemonStatus) bool {
		return s.Subscribers >= 1
	})

	// A port appears.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if uri := awaitUpdate(t, updates, 20*time.Second); uri != mcpserver.URIPorts {
		t.Fatalf("update = %q, want %s", uri, mcpserver.URIPorts)
	}
	t.Logf("a new listener on %d produced resources/updated for %s",
		ln.Addr().(*net.TCPAddr).Port, mcpserver.URIPorts)

	// And goes away again.
	drain(updates)
	_ = ln.Close()
	if uri := awaitUpdate(t, updates, 20*time.Second); uri != mcpserver.URIPorts {
		t.Fatalf("update after the listener closed = %q, want %s", uri, mcpserver.URIPorts)
	}
	t.Log("closing the listener produced a second resources/updated")

	if err := session.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: mcpserver.URIPorts}); err != nil {
		t.Fatalf("unsubscribing: %v", err)
	}
	waitForStatus(t, e, "the daemon's subscriber count to go back to zero", func(s daemonStatus) bool {
		return s.Subscribers == 0
	})
	t.Log("after unsubscribing `sonar daemon status` reports 0 subscribers")
}

// TestRealDaemonPromptsOverStdio is the prompt half of the demo.
func TestRealDaemonPromptsOverStdio(t *testing.T) {
	e := newRealEnv(t)
	session := e.connect(t)

	list, err := session.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatalf("prompts/list: %v", err)
	}
	if len(list.Prompts) != 2 {
		t.Fatalf("prompts/list = %+v, want two prompts", list.Prompts)
	}

	res, err := session.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name:      mcpserver.PromptFreePort,
		Arguments: map[string]string{"port": "8123"},
	})
	if err != nil {
		t.Fatalf("prompts/get free_port: %v", err)
	}
	text, ok := res.Messages[0].Content.(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "Inspect port 8123") {
		t.Fatalf("free_port expanded to %+v", res.Messages)
	}
	t.Logf("prompts/get free_port {port: 8123} →\n%s", text.Text)
}

type daemonStatus struct {
	Running     bool `json:"running"`
	Subscribers int  `json:"subscribers"`
}

func waitForStatus(t *testing.T, e *realEnv, what string, ok func(daemonStatus) bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last daemonStatus
	for time.Now().Before(deadline) {
		out := e.run(t, "daemon", "status", "--json")
		if err := json.Unmarshal([]byte(out), &last); err == nil && ok(last) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (last status: %+v)", what, last)
}

func awaitUpdate(t *testing.T, updates chan string, limit time.Duration) string {
	t.Helper()
	select {
	case uri := <-updates:
		return uri
	case <-time.After(limit):
		t.Fatal("no resources/updated arrived")
		return ""
	}
}

func drain(updates chan string) {
	for {
		select {
		case <-updates:
		default:
			return
		}
	}
}

func contains(all []string, want string) bool {
	for _, v := range all {
		if v == want {
			return true
		}
	}
	return false
}

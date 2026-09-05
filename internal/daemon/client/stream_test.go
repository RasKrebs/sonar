package client_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/scanner"
)

// serveTestDaemon starts a daemon on a socket in a temp directory, with a
// scanner that reports nothing, and returns a connected client.
func serveTestDaemon(t *testing.T) *client.Client {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}

	// Not t.TempDir(): its path carries the test name, and a unix socket path
	// is capped at ~104 bytes on macOS.
	dir, err := os.MkdirTemp("", "sn")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	srv := daemon.New(daemon.Options{
		Socket:  socket,
		Version: "test",
		Scanner: scanner.New(scanner.Options{
			DaemonVersion: "test",
			Scan:          func(scanner.Include) ([]ports.ListeningPort, error) { return nil, nil },
		}),
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-srv.Done()
	})

	if err := client.WaitForSocket(ctx, socket, 5*time.Second); err != nil {
		t.Fatalf("daemon did not come up: %v", err)
	}
	c, err := client.Dial(ctx, client.ClientInfo{Name: "cli", Version: "test", Socket: socket})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// freePort opens a listener and returns it with its port, so ports.wait has
// something that is genuinely ready.
func freePort(t *testing.T) (net.Listener, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return ln, port
}

// TestStreamDeliversChunksThenEnd drives a real streaming method end to end:
// the reply carries the subscription id, chunks arrive on Chunks, and End
// carries the method's final payload.
func TestStreamDeliversChunksThenEnd(t *testing.T) {
	c := serveTestDaemon(t)
	_, port := freePort(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := c.Stream(ctx, "ports.wait", rpc.PortsWaitParams{
		Ports: []int{port}, TimeoutMs: 5000, IntervalMs: 20,
	}, nil)
	if err != nil {
		t.Fatalf("ports.wait: %v", err)
	}
	if s.ID() == "" {
		t.Fatal("no subscription id")
	}

	var chunks []rpc.PortsWaitChunk
	for raw := range s.Chunks() {
		var chunk rpc.PortsWaitChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			t.Fatalf("decoding chunk: %v", err)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) != 1 || chunks[0].Port != port {
		t.Fatalf("chunks = %+v, want one for port %d", chunks, port)
	}

	end := <-s.End()
	if end.Err != nil {
		t.Fatalf("stream ended with an error: %v", end.Err)
	}
	var final rpc.PortsWaitEnd
	if err := end.Decode(&final); err != nil {
		t.Fatalf("decoding end: %v", err)
	}
	if len(final.Ready) != 1 || final.Ready[0] != port {
		t.Fatalf("end = %+v, want ready [%d]", final, port)
	}
}

// TestStreamCancelEndsTheStream checks the client side of stream.cancel: the
// daemon stops producing and still closes the stream cleanly.
func TestStreamCancelEndsTheStream(t *testing.T) {
	c := serveTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Nothing is listening on this port, so the stream stays open until the
	// timeout — or until we cancel it.
	s, err := c.Stream(ctx, "ports.wait", rpc.PortsWaitParams{
		Ports: []int{1}, TimeoutMs: 60000, IntervalMs: 50,
	}, nil)
	if err != nil {
		t.Fatalf("ports.wait: %v", err)
	}
	if err := s.Cancel(ctx); err != nil {
		t.Fatalf("stream.cancel: %v", err)
	}

	select {
	case end := <-s.End():
		if end.Err != nil {
			t.Fatalf("a cancelled stream should end cleanly, got %v", end.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never ended after stream.cancel")
	}

	// Cancelling again is a no-op, not an error the caller has to handle.
	if err := s.Cancel(ctx); err != nil {
		t.Fatalf("cancelling twice: %v", err)
	}
}

// TestStreamEndsWhenTheConnectionDrops makes sure a caller blocked on End is
// released, with an error, when the daemon goes away.
func TestStreamEndsWhenTheConnectionDrops(t *testing.T) {
	c := serveTestDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := c.Stream(ctx, "ports.wait", rpc.PortsWaitParams{
		Ports: []int{1}, TimeoutMs: 60000, IntervalMs: 50,
	}, nil)
	if err != nil {
		t.Fatalf("ports.wait: %v", err)
	}
	c.Close()

	select {
	case end := <-s.End():
		if end.Err == nil {
			t.Fatal("a dropped connection should end the stream with an error")
		}
		if !strings.Contains(end.Err.Error(), "closed") {
			t.Logf("end error: %v", end.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never ended after the connection dropped")
	}
}

// TestUnaryLogsRejectsAnUnknownPort keeps the unary half of ports.logs honest
// through the real client: it is a plain call, not a stream.
func TestUnaryLogsRejectsAnUnknownPort(t *testing.T) {
	c := serveTestDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	port := 1
	var out rpc.PortsLogsResult
	err := c.Call(ctx, "ports.logs", rpc.PortsLogsParams{
		Selector: rpc.Selector{Port: &port},
	}, &out)
	if err == nil {
		t.Fatal("logs for an unlistened port should fail")
	}
}

// test.instant is a streaming method that finishes before it has really
// started: registered once, from init, because the dispatcher rejects a
// duplicate and `-count=2` would otherwise panic.
func init() {
	daemon.RegisterHandler("test.instant", func(ctx context.Context, req *daemon.Request) (any, error) {
		return daemon.StartStream(ctx, req, nil, func(context.Context, *daemon.Stream) (any, error) {
			return map[string]any{"done": true}, nil
		})
	})
}

// TestStreamThatEndsBeforeItsReplyIsDecoded is the race `sonar up` walks into
// every time it starts a group whose services are all already running: the
// daemon replies, sends its chunks and ends the stream faster than the client
// decodes the reply. The client has to hand back the stream those chunks went
// to, not a fresh empty one that never ends.
func TestStreamThatEndsBeforeItsReplyIsDecoded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}
	c := serveTestDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Ten rounds: the interleaving is a race, and one pass proves little.
	for i := 0; i < 10; i++ {
		s, err := c.Stream(ctx, "test.instant", rpc.Empty{}, nil)
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		for range s.Chunks() {
			// Drained; this method sends none.
		}
		select {
		case end, ok := <-s.End():
			if !ok {
				t.Fatalf("round %d: the end channel closed without an end", i)
			}
			if end.Err != nil {
				t.Fatalf("round %d: %v", i, end.Err)
			}
			var payload struct {
				Done bool `json:"done"`
			}
			if err := end.Decode(&payload); err != nil || !payload.Done {
				t.Fatalf("round %d: end payload = %s (%v)", i, end.Data, err)
			}
		case <-ctx.Done():
			t.Fatalf("round %d: the stream never ended", i)
		}
		s.Close()
	}
}

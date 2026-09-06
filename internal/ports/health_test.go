package ports

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestIsConnectionRefused(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "connection refused",
			err:  errors.New("connection refused"),
			want: true,
		},
		{
			name: "wrapped in url.Error",
			err:  &url.Error{Op: "Get", URL: "http://localhost:9999", Err: errors.New("connection refused")},
			want: true,
		},
		{
			name: "timeout error",
			err:  errors.New("i/o timeout"),
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("something else went wrong"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConnectionRefused(tt.err); got != tt.want {
				t.Errorf("isConnectionRefused(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestEnrichHealthRoundIsBudgeted is step 1A.19. Ten probes run at once, so
// forty listeners that accept and never answer used to cost four waves of the
// per-probe timeout — inside the scan a kill or a `.sonar.yaml` write is queued
// behind. The round is bounded now; the ports it did not reach keep whatever
// the previous tick found (see carryHealth in the scanner).
func TestEnrichHealthRoundIsBudgeted(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	defer ln.Close()
	// Accept and never answer, which is what makes a probe wait out its
	// timeout rather than fail fast.
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	pp := make([]ListeningPort, 40)
	for i := range pp {
		pp[i] = ListeningPort{Port: port, BindAddress: "127.0.0.1"}
	}

	start := time.Now()
	EnrichHealth(pp, time.Second, 300*time.Millisecond)
	took := time.Since(start)

	if took > 2*time.Second {
		t.Fatalf("a round of 40 probes took %s, want it bounded by the budget", took)
	}
}

func TestProbeBudget(t *testing.T) {
	if d, ok := ProbeBudget(2*time.Second, time.Time{}); !ok || d != 2*time.Second {
		t.Errorf("no deadline: got %s/%v, want the full timeout", d, ok)
	}
	if _, ok := ProbeBudget(time.Second, time.Now().Add(-time.Millisecond)); ok {
		t.Error("a spent budget should not start another probe")
	}
	d, ok := ProbeBudget(time.Second, time.Now().Add(50*time.Millisecond))
	if !ok || d > 50*time.Millisecond {
		t.Errorf("near the deadline: got %s/%v, want the timeout clamped to what is left", d, ok)
	}
}

package ports

import (
	"reflect"
	"testing"
)

// One `lsof -iTCP -sTCP:ESTABLISHED` must answer for every port at once, and
// answer with the numbers the old per-port calls did: `-i:<port>` matched a
// connection at either end, so a loopback pair counted twice and was halved.
func TestCountLsofByPort(t *testing.T) {
	out := `COMMAND   PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
node     1000 rkrebs   24u  IPv4 0x1111      0t0  TCP 127.0.0.1:3000->127.0.0.1:52344 (ESTABLISHED)
curl     1001 rkrebs    5u  IPv4 0x1111      0t0  TCP 127.0.0.1:52344->127.0.0.1:3000 (ESTABLISHED)
node     1000 rkrebs   25u  IPv4 0x2222      0t0  TCP 127.0.0.1:3000->127.0.0.1:52345 (ESTABLISHED)
curl     1002 rkrebs    5u  IPv4 0x2222      0t0  TCP 127.0.0.1:52345->127.0.0.1:3000 (ESTABLISHED)
vite     1003 rkrebs   30u  IPv6 0x3333      0t0  TCP [::1]:5173->[::1]:60001 (ESTABLISHED)
chrome   1004 rkrebs   88u  IPv6 0x3333      0t0  TCP [::1]:60001->[::1]:5173 (ESTABLISHED)
sshd      900 root      3u  IPv4 0x4444      0t0  TCP 10.0.0.2:22->10.0.0.9:51000 (ESTABLISHED)
`
	got := countLsofByPort(out)
	for _, tt := range []struct {
		port, want int
	}{
		{3000, 2}, // two loopback clients
		{5173, 1}, // one, over IPv6
		{22, 0},   // remote peer: only one end is local, as before
	} {
		if got[tt.port] != tt.want {
			t.Errorf("port %d = %d connections, want %d (all: %v)", tt.port, got[tt.port], tt.want, got)
		}
	}
}

func TestCountSSByPort(t *testing.T) {
	// With a state filter ss drops the State column; without one it keeps it.
	// Both shapes have to read the same.
	filtered := `Recv-Q Send-Q Local Address:Port  Peer Address:Port
0      0      127.0.0.1:3000      127.0.0.1:52344
0      0      127.0.0.1:3000      127.0.0.1:52345
0      0      127.0.0.1:52344     127.0.0.1:3000
0      0      [::1]:5173          [::1]:60001
`
	unfiltered := `State  Recv-Q Send-Q Local Address:Port  Peer Address:Port
ESTAB  0      0      127.0.0.1:3000      127.0.0.1:52344
ESTAB  0      0      127.0.0.1:3000      127.0.0.1:52345
ESTAB  0      0      127.0.0.1:52344     127.0.0.1:3000
ESTAB  0      0      [::1]:5173          [::1]:60001
`
	if !reflect.DeepEqual(countSSByPort(filtered), countSSByPort(unfiltered)) {
		t.Errorf("ss with and without its State column read differently:\n %v\n %v",
			countSSByPort(filtered), countSSByPort(unfiltered))
	}
	got := countSSByPort(filtered)
	// ss reports the local end as the source, which is what the per-port
	// `sport = :<port>` filter selected: no halving.
	if got[3000] != 2 || got[5173] != 1 || got[52344] != 1 {
		t.Errorf("counts = %v, want 3000:2 5173:1 52344:1", got)
	}
}

func TestCountNetstatByPort(t *testing.T) {
	out := `
Active Connections
  Proto  Local Address          Foreign Address        State           PID
  TCP    127.0.0.1:3000         127.0.0.1:52344        ESTABLISHED     1000
  TCP    127.0.0.1:3000         127.0.0.1:52345        ESTABLISHED     1000
  TCP    0.0.0.0:5173           0.0.0.0:0              LISTENING       1003
`
	got := countNetstatByPort(out)
	if got[3000] != 2 {
		t.Errorf("port 3000 = %d, want 2 (all: %v)", got[3000], got)
	}
	if got[5173] != 0 {
		t.Errorf("a LISTENING row was counted as a connection: %v", got)
	}
}

// `ps -M` with a pid list: a process line then one line per thread, and the
// pid sits in a different column on each. The count is every line but the
// header, per pid, which is what the one-fork-per-pid version reported.
func TestCountPSThreads(t *testing.T) {
	out := `USER     PID   TT   %CPU STAT PRI     STIME     UTIME COMMAND
root       1   ??    0.0 S    31T   0:00.40   0:00.09 /sbin/launchd
           1         0.0 S    37T   0:00.02   0:00.02 
           1         0.1 S    37T   0:00.01   0:00.01 
rkrebs  1000   ??    3.1 S    31T   0:00.01   0:00.01 node server.js
        1000         0.0 S    37T   0:00.00   0:00.00 
`
	got := countPSThreads(out, map[int]bool{1: true, 1000: true})
	want := map[int]int{1: 3, 1000: 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("thread counts = %v, want %v", got, want)
	}
}

// A pid ps had nothing to say about is absent rather than reported as zero
// threads by a line that belongs to someone else.
func TestCountPSThreadsIgnoresUnrequestedPIDs(t *testing.T) {
	out := `USER     PID   TT   %CPU STAT PRI     STIME     UTIME COMMAND
rkrebs  1000   ??    3.1 S    31T   0:00.01   0:00.01 node server.js
        1000         0.0 S    37T   0:00.00   0:00.00 
`
	got := countPSThreads(out, map[int]bool{1000: true, 2000: true})
	if _, ok := got[2000]; ok {
		t.Errorf("counts = %v, want no entry for the pid ps did not report", got)
	}
	if got[1000] != 2 {
		t.Errorf("pid 1000 = %d threads, want 2", got[1000])
	}
}

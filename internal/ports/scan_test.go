package ports

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseLsof_MultipleBindsSamePort(t *testing.T) {
	output := `COMMAND   PID  USER   FD   TYPE   DEVICE SIZE/OFF NODE NAME
nginx   12345 root    6u  IPv4  1234567      0t0  TCP 192.168.2.5:80 (LISTEN)
nginx   12345 root    7u  IPv4  1234568      0t0  TCP 172.31.96.5:80 (LISTEN)
nginx   12345 root    8u  IPv4  1234569      0t0  TCP 192.168.4.5:443 (LISTEN)
nginx   12345 root    9u  IPv4  1234570      0t0  TCP 192.168.4.5:80 (LISTEN)
`
	results := parseLsof(output)

	if len(results) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(results))
	}

	// Verify all bind addresses are present
	binds := map[string]bool{}
	for _, r := range results {
		binds[r.BindAddress+":"+itoa(r.Port)] = true
	}
	expected := []string{"192.168.2.5:80", "172.31.96.5:80", "192.168.4.5:443", "192.168.4.5:80"}
	for _, e := range expected {
		if !binds[e] {
			t.Errorf("expected bind %s not found in results", e)
		}
	}
}

func TestParseLsof_DedupsSamePortAndBind(t *testing.T) {
	output := `COMMAND   PID  USER   FD   TYPE   DEVICE SIZE/OFF NODE NAME
node    1234 user    6u  IPv4  1234567      0t0  TCP *:3000 (LISTEN)
node    1234 user    7u  IPv4  1234568      0t0  TCP *:3000 (LISTEN)
`
	results := parseLsof(output)

	if len(results) != 1 {
		t.Fatalf("expected 1 entry (deduped), got %d", len(results))
	}
	if results[0].BindAddress != "0.0.0.0" {
		t.Errorf("expected bind 0.0.0.0, got %s", results[0].BindAddress)
	}
}

func TestParseSS_ProcessNameWithSpaces(t *testing.T) {
	// Next.js sets process.title to "next-server (v16.2.6)"; Linux truncates
	// comm to 15 bytes ("next-server (v1"), so ss -p emits a quoted name that
	// contains a space. strings.Fields must not be used to parse that blob.
	output := `State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process
LISTEN 0      511    *:3011             *:*    users:(("next-server (v1",pid=113211,fd=22))
LISTEN 0      128    127.0.0.1:3000     0.0.0.0:* users:(("node",pid=1234,fd=18))
LISTEN 0      128    *:8080             *:*
`
	results := parseSS(output)
	if len(results) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(results))
	}

	byPort := map[int]ListeningPort{}
	for _, r := range results {
		byPort[r.Port] = r
	}

	next := byPort[3011]
	if next.PID != 113211 {
		t.Errorf("port 3011 PID = %d, want 113211", next.PID)
	}
	if next.Process != "next-server (v1" {
		t.Errorf("port 3011 process = %q, want %q", next.Process, "next-server (v1")
	}
	if next.BindAddress != "0.0.0.0" {
		t.Errorf("port 3011 bind = %q, want 0.0.0.0", next.BindAddress)
	}

	node := byPort[3000]
	if node.PID != 1234 || node.Process != "node" {
		t.Errorf("port 3000 = pid %d name %q, want pid 1234 name node", node.PID, node.Process)
	}

	idle := byPort[8080]
	if idle.PID != 0 || idle.Process != "" {
		t.Errorf("port 8080 = pid %d name %q, want empty users blob", idle.PID, idle.Process)
	}
}

func TestParseSS_UsersBlobVariants(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		wantProcess string
		wantPID     int
	}{
		{
			name:        "one entry per file descriptor",
			line:        `LISTEN 0      128    0.0.0.0:80    0.0.0.0:*    users:(("nginx",pid=1234,fd=6),("nginx",pid=1235,fd=6))`,
			wantProcess: "nginx",
			wantPID:     1234,
		},
		{
			// A process title can contain anything, including "pid=". The real
			// PID is the one after the closing quote, not the one in the name.
			name:        "name containing pid=",
			line:        `LISTEN 0      511    *:3011        *:*          users:(("worker pid=7",pid=99001,fd=22))`,
			wantProcess: "worker pid=7",
			wantPID:     99001,
		},
		{
			name:        "iproute2 without pid label",
			line:        `LISTEN 0      128    0.0.0.0:22    0.0.0.0:*    users:(("sshd",987,3))`,
			wantProcess: "sshd",
			wantPID:     987,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := "State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process\n" + tt.line + "\n"
			results := parseSS(output)

			if len(results) != 1 {
				t.Fatalf("expected 1 entry, got %d", len(results))
			}
			if results[0].Process != tt.wantProcess {
				t.Errorf("Process = %q, want %q", results[0].Process, tt.wantProcess)
			}
			if results[0].PID != tt.wantPID {
				t.Errorf("PID = %d, want %d", results[0].PID, tt.wantPID)
			}
		})
	}
}

func TestParseSS_MultipleBindsSamePort(t *testing.T) {
	output := `State  Recv-Q Send-Q Local Address:Port  Peer Address:Port Process
LISTEN 0      128    127.0.0.2:8080       0.0.0.0:*     users:(("nc",pid=1001,fd=3))
LISTEN 0      128    127.0.0.3:8080       0.0.0.0:*     users:(("nc",pid=1002,fd=3))
LISTEN 0      128    127.0.0.4:8080       0.0.0.0:*     users:(("nc",pid=1003,fd=3))
`
	results := parseSS(output)

	if len(results) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(results))
	}

	addrs := map[string]bool{}
	for _, r := range results {
		addrs[r.BindAddress] = true
	}
	for _, addr := range []string{"127.0.0.2", "127.0.0.3", "127.0.0.4"} {
		if !addrs[addr] {
			t.Errorf("expected bind address %s not found", addr)
		}
	}
}

func TestParseNetstat_MultipleBindsSamePort(t *testing.T) {
	output := `  Proto  Local Address          Foreign Address        State           PID
  TCP    192.168.1.1:8080       0.0.0.0:0              LISTENING       1001
  TCP    192.168.1.2:8080       0.0.0.0:0              LISTENING       1002
  TCP    192.168.1.3:8080       0.0.0.0:0              LISTENING       1003
`
	results := parseNetstat(output)

	if len(results) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(results))
	}

	addrs := map[string]bool{}
	for _, r := range results {
		addrs[r.BindAddress] = true
	}
	for _, addr := range []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"} {
		if !addrs[addr] {
			t.Errorf("expected bind address %s not found", addr)
		}
	}
}

func TestPortKey(t *testing.T) {
	lp := ListeningPort{Port: 8080, BindAddress: "127.0.0.1"}
	if lp.PortKey() != "8080:127.0.0.1" {
		t.Errorf("expected '8080:127.0.0.1', got '%s'", lp.PortKey())
	}
}

func TestURL_UsesBindAddress(t *testing.T) {
	tests := []struct {
		bind string
		want string
	}{
		{"0.0.0.0", "http://localhost:3000"},
		{"", "http://localhost:3000"},
		{"[::]", "http://localhost:3000"},
		{"::", "http://localhost:3000"},
		{"::1", "http://[::1]:3000"},
		{"192.168.1.5", "http://192.168.1.5:3000"},
		{"127.0.0.1", "http://127.0.0.1:3000"},
	}
	for _, tt := range tests {
		lp := ListeningPort{Port: 3000, BindAddress: tt.bind}
		got := lp.URL()
		if got != tt.want {
			t.Errorf("URL() with bind %q = %q, want %q", tt.bind, got, tt.want)
		}
	}
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

// TestNormalizeBind pins contract §21: bind_address and ip_version always
// agree. A dual-stack listener used to report "0.0.0.0" with "IPv6".
func TestNormalizeBind(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		v6      bool
		bind    string
		version string
	}{
		{"lsof v4 wildcard", "*", false, "0.0.0.0", "IPv4"},
		{"lsof v6 wildcard", "*", true, "::", "IPv6"},
		{"netstat v6 wildcard", "[::]", true, "::", "IPv6"},
		{"ss v6 wildcard", "[::]", false, "::", "IPv6"},
		{"bare v6 wildcard", "::", false, "::", "IPv6"},
		{"v4 wildcard spelled out", "0.0.0.0", false, "0.0.0.0", "IPv4"},
		{"v4 loopback", "127.0.0.1", false, "127.0.0.1", "IPv4"},
		{"v6 loopback", "[::1]", false, "::1", "IPv6"},
		{"v6 literal", "[fe80::1]", true, "fe80::1", "IPv6"},
		{"lan address", "192.168.1.5", false, "192.168.1.5", "IPv4"},
		{"empty is the v4 wildcard", "", false, "0.0.0.0", "IPv4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bind, version := normalizeBind(tt.raw, tt.v6)
			if bind != tt.bind || version != tt.version {
				t.Errorf("normalizeBind(%q, %v) = (%q, %q), want (%q, %q)",
					tt.raw, tt.v6, bind, version, tt.bind, tt.version)
			}
			wantV6 := version == "IPv6"
			if gotV6 := strings.Contains(bind, ":"); gotV6 != wantV6 {
				t.Errorf("bind %q and ip_version %q disagree", bind, version)
			}
		})
	}
}

// TestParseLsof_DualStackWildcard is the smoke-test regression: a process
// listening on both families yields one row per family, each self-consistent.
func TestParseLsof_DualStackWildcard(t *testing.T) {
	output := `COMMAND   PID  USER   FD   TYPE   DEVICE SIZE/OFF NODE NAME
node    1234 user    6u  IPv4  1234567      0t0  TCP *:3000 (LISTEN)
node    1234 user    7u  IPv6  1234568      0t0  TCP *:3000 (LISTEN)
node    1234 user    8u  IPv6  1234569      0t0  TCP [::1]:4000 (LISTEN)
`
	results := parseLsof(output)
	if len(results) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(results), results)
	}
	want := map[string]string{
		"0.0.0.0": "IPv4",
		"::":      "IPv6",
		"::1":     "IPv6",
	}
	for _, r := range results {
		version, ok := want[r.BindAddress]
		if !ok {
			t.Errorf("unexpected bind address %q", r.BindAddress)
			continue
		}
		if r.IPVersion != version {
			t.Errorf("bind %q reported ip_version %q, want %q", r.BindAddress, r.IPVersion, version)
		}
	}
}

// TestParseNetstat_DualStackWildcard covers the same thing on Windows, where
// the v6 wildcard used to be folded onto the v4 row.
func TestParseNetstat_DualStackWildcard(t *testing.T) {
	output := `  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:8000           0.0.0.0:0              LISTENING       1001
  TCP    [::]:8000              [::]:0                 LISTENING       1001
`
	results := parseNetstat(output)
	if len(results) != 2 {
		t.Fatalf("expected 2 entries (one per family), got %d: %+v", len(results), results)
	}
	byBind := map[string]string{}
	for _, r := range results {
		byBind[r.BindAddress] = r.IPVersion
	}
	if byBind["0.0.0.0"] != "IPv4" {
		t.Errorf("0.0.0.0 reported %q, want IPv4", byBind["0.0.0.0"])
	}
	if byBind["::"] != "IPv6" {
		t.Errorf(":: reported %q, want IPv6", byBind["::"])
	}
}

// TestParseNetstat_RealCapture runs the parser over a verbatim `netstat -ano`
// capture: the two header lines, IPv4 and IPv6 listeners, established
// connections that must be ignored, a LISTENING row owned by pid 0 and a dev
// server. The Windows daemon sees no ports at all when this parser is wrong,
// so it is pinned against real output rather than a hand-shaped sample.
func TestParseNetstat_RealCapture(t *testing.T) {
	output := "\r\nActive Connections\r\n\r\n" +
		"  Proto  Local Address          Foreign Address        State           PID\r\n" +
		"  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1064\r\n" +
		"  TCP    0.0.0.0:445            0.0.0.0:0              LISTENING       4\r\n" +
		"  TCP    0.0.0.0:5040           0.0.0.0:0              LISTENING       0\r\n" +
		"  TCP    127.0.0.1:49670        127.0.0.1:49671        ESTABLISHED     7276\r\n" +
		"  TCP    127.0.0.1:52341        0.0.0.0:0              LISTENING       7276\r\n" +
		"  TCP    10.1.0.4:49675         20.209.14.65:443       ESTABLISHED     3812\r\n" +
		"  TCP    [::]:135               [::]:0                 LISTENING       1064\r\n" +
		"  TCP    [::1]:52341            [::]:0                 LISTENING       7276\r\n" +
		"  TCP    [::]:445               [::]:0                 TIME_WAIT       0\r\n" +
		"  UDP    0.0.0.0:5353           *:*                                    2648\r\n"

	got := parseNetstat(output)
	if len(got) != 6 {
		t.Fatalf("parsed %d rows, want the 6 LISTENING ones: %+v", len(got), got)
	}

	type row struct {
		pid     int
		bind    string
		version string
	}
	byKey := map[string]row{}
	for _, r := range got {
		byKey[r.PortKey()] = row{r.PID, r.BindAddress, r.IPVersion}
	}
	want := map[string]row{
		"135:0.0.0.0":     {1064, "0.0.0.0", "IPv4"},
		"445:0.0.0.0":     {4, "0.0.0.0", "IPv4"},
		"5040:0.0.0.0":    {0, "0.0.0.0", "IPv4"},
		"52341:127.0.0.1": {7276, "127.0.0.1", "IPv4"},
		"135:::":          {1064, "::", "IPv6"},
		"52341:::1":       {7276, "::1", "IPv6"},
	}
	for key, w := range want {
		g, ok := byKey[key]
		if !ok {
			t.Errorf("%s missing from the parse: %+v", key, byKey)
			continue
		}
		if g != w {
			t.Errorf("%s = %+v, want %+v", key, g, w)
		}
	}
	for _, unwanted := range []string{"49670:127.0.0.1", "49675:10.1.0.4", "445:::", "5353:0.0.0.0"} {
		if _, ok := byKey[unwanted]; ok {
			t.Errorf("%s was parsed; only TCP LISTENING rows belong in a scan", unwanted)
		}
	}
}

package state

import (
	"encoding/json"
	"testing"
)

func hostRow(name string) Host {
	return Host{
		Name: name, Address: name, Status: HostConnected,
		DaemonVersion: "0.5.1", ProtocolVersion: "1.0.0",
		OS: "linux", Arch: "amd64", Kernel: "6.8.0-40-generic",
		UptimeS: i64(93607), CPUPercent: f64(12.4),
		Load:       []float64{1.24, 0.98, 0.71},
		MemoryUsed: i64(9 << 30), MemoryTotal: i64(32 << 30),
		DiskUsed: i64(412 << 30), DiskTotal: i64(931 << 30), DiskPath: "/",
		Ports: 6, Groups: 2, LastSeen: "2026-09-06T10:00:00Z",
	}
}

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }

// Hosts are keyed by name, so the same machine reporting new load is an update
// and never an add-plus-remove.
func TestDiffKeysHostsByName(t *testing.T) {
	prev := Snapshot{Hosts: []Host{hostRow("localhost"), hostRow("hetzner")}}
	next := Snapshot{Hosts: []Host{hostRow("localhost")}}
	next.Hosts[0].CPUPercent = f64(80)

	d := Diff(prev, next)
	if len(d.Hosts.Updated) != 1 || d.Hosts.Updated[0].Name != "localhost" {
		t.Fatalf("updated = %+v, want the localhost row", d.Hosts.Updated)
	}
	if len(d.Hosts.Removed) != 1 || d.Hosts.Removed[0] != "hetzner" {
		t.Fatalf("removed = %v, want [hetzner]", d.Hosts.Removed)
	}
	if len(d.Hosts.Added) != 0 {
		t.Fatalf("added = %+v, want none", d.Hosts.Added)
	}
}

// Host load is state, not an opt-in statistic: a stats-only change is an
// update for a subscriber that asked for stats and for one that did not, so
// the machine's load reaches every client (contract §22's rule for configured
// health).
func TestHostStatsChangeIsNotGatedByInclude(t *testing.T) {
	prev := Snapshot{Hosts: []Host{hostRow("localhost")}}
	next := Snapshot{Hosts: []Host{hostRow("localhost")}}
	next.Hosts[0].CPUPercent = f64(80)
	next.Hosts[0].MemoryUsed = i64(11 << 30)

	for name, d := range map[string]Delta{
		"Diff":          Diff(prev, next),
		"DiffWithStats": DiffWithStats(prev, next),
	} {
		if len(d.Hosts.Updated) != 1 {
			t.Errorf("%s published %d host updates, want 1", name, len(d.Hosts.Updated))
			continue
		}
		if got := *d.Hosts.Updated[0].CPUPercent; got != 80 {
			t.Errorf("%s published cpu_percent %v, want 80", name, got)
		}
	}
}

// last_seen and latency move on every tick by construction. Comparing them
// would publish a `hosts` delta forever on a machine where nothing happens —
// the reason §25 keeps health latency out of the port comparison.
func TestDiffIgnoresLastSeenAndLatencyAlone(t *testing.T) {
	prev := Snapshot{Hosts: []Host{hostRow("localhost")}}
	next := Snapshot{Hosts: []Host{hostRow("localhost")}}
	next.Hosts[0].LastSeen = "2026-09-06T10:00:02Z"
	next.Hosts[0].LatencyMs = 37

	if d := Diff(prev, next); len(d.Hosts.Updated) != 0 {
		t.Fatalf("a tick that only moved the clock published %+v", d.Hosts.Updated)
	}

	// A real change carries the newest clock and latency out with it.
	next.Hosts[0].CPUPercent = f64(80)
	d := Diff(prev, next)
	if len(d.Hosts.Updated) != 1 {
		t.Fatalf("updated = %+v, want one row", d.Hosts.Updated)
	}
	if d.Hosts.Updated[0].LastSeen != "2026-09-06T10:00:02Z" || d.Hosts.Updated[0].LatencyMs != 37 {
		t.Errorf("the published row lost its newest clock: %+v", d.Hosts.Updated[0])
	}
}

// Every collection marshals as an array, never null (contract §5).
func TestHostChangeMarshalsAsArrays(t *testing.T) {
	raw, err := json.Marshal(Diff(Snapshot{}, Snapshot{}).Hosts)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"added":[],"updated":[],"removed":[]}` {
		t.Errorf("empty host change = %s", got)
	}
}

// Nullable fields are null on the wire, not zero: a machine whose load could
// not be read must not look idle.
func TestHostNullsSurviveJSON(t *testing.T) {
	raw, err := json.Marshal(Host{Name: LocalhostName})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"status_reason", "uptime_s", "cpu_percent", "load",
		"memory_used_bytes", "memory_total_bytes", "disk_used_bytes", "disk_total_bytes",
	} {
		v, ok := got[key]
		if !ok {
			t.Errorf("%s is absent; it must be present and null", key)
			continue
		}
		if v != nil {
			t.Errorf("%s = %v, want null", key, v)
		}
	}
}

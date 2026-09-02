package state

import "testing"

func TestDiffAddedUpdatedRemovedAndRestart(t *testing.T) {
	prev := Snapshot{Seq: 1, Ports: []Port{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 10, DisplayName: "a"},
		{Port: 5432, BindAddress: "0.0.0.0", PID: 20, DisplayName: "db"},
	}}
	next := Snapshot{Seq: 2, Ports: []Port{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 11, DisplayName: "a"}, // pid changed = restart
		{Port: 5432, BindAddress: "0.0.0.0", PID: 20, DisplayName: "database"},
		{Port: 8080, BindAddress: "0.0.0.0", PID: 30, DisplayName: "new"},
	}}
	d := Diff(prev, next)
	if len(d.Ports.Added) != 2 { // 8080 and restarted 3000
		t.Fatalf("added = %d, want 2", len(d.Ports.Added))
	}
	if len(d.Ports.Removed) != 1 || d.Ports.Removed[0] != "3000:127.0.0.1" {
		t.Fatalf("removed = %v, want [3000:127.0.0.1]", d.Ports.Removed)
	}
	if len(d.Ports.Updated) != 1 || d.Ports.Updated[0].DisplayName != "database" {
		t.Fatalf("updated = %+v", d.Ports.Updated)
	}
	if d.Seq != 2 {
		t.Fatalf("seq = %d", d.Seq)
	}
}

func TestDiffStatsOnlyChangeIsNotAnUpdateWithoutStats(t *testing.T) {
	prev := Snapshot{Ports: []Port{{Port: 1, BindAddress: "x", PID: 1, Stats: &Stats{CPUPercent: 1}}}}
	next := Snapshot{Ports: []Port{{Port: 1, BindAddress: "x", PID: 1, Stats: &Stats{CPUPercent: 2}}}}
	if d := Diff(prev, next); len(d.Ports.Updated) != 0 {
		t.Fatalf("stats-only change must not produce Updated, got %+v", d.Ports.Updated)
	}
	if d := DiffWithStats(prev, next); len(d.Ports.Updated) != 1 {
		t.Fatalf("DiffWithStats must report stats change")
	}
}

func TestDiffGroupsComparedByStatusAndMembers(t *testing.T) {
	prev := Snapshot{Groups: []Group{{Name: "a", Status: "running", Members: []int{1}}}}
	next := Snapshot{Groups: []Group{
		{Name: "a", Status: "partial", Members: []int{1}},
		{Name: "b", Status: "stopped"},
	}}
	d := Diff(prev, next)
	if len(d.Groups.Added) != 1 || d.Groups.Added[0].Name != "b" {
		t.Fatalf("added = %+v", d.Groups.Added)
	}
	if len(d.Groups.Updated) != 1 || d.Groups.Updated[0].Status != "partial" {
		t.Fatalf("updated = %+v", d.Groups.Updated)
	}
	if len(d.Groups.Removed) != 0 {
		t.Fatalf("removed = %v", d.Groups.Removed)
	}
}

func TestDiffTunnelsProxiesSessionsByID(t *testing.T) {
	prev := Snapshot{
		Tunnels:  []Tunnel{{ID: "t1", Status: "ready"}},
		Proxies:  []Proxy{{ID: "p1", ListenPort: 3002}},
		Sessions: []SessionRecord{{Session: Session{ID: "s1"}, Runs: 1}},
	}
	next := Snapshot{
		Tunnels:  []Tunnel{{ID: "t1", Status: "degraded"}},
		Proxies:  []Proxy{},
		Sessions: []SessionRecord{{Session: Session{ID: "s1"}, Runs: 1}, {Session: Session{ID: "s2"}}},
	}
	d := Diff(prev, next)
	if len(d.Tunnels.Updated) != 1 {
		t.Fatalf("tunnels updated = %+v", d.Tunnels.Updated)
	}
	if len(d.Proxies.Removed) != 1 || d.Proxies.Removed[0] != "p1" {
		t.Fatalf("proxies removed = %v", d.Proxies.Removed)
	}
	if len(d.Sessions.Added) != 1 || len(d.Sessions.Updated) != 0 {
		t.Fatalf("sessions = %+v", d.Sessions)
	}
}

func TestDiffCarriesExposuresActive(t *testing.T) {
	if d := Diff(Snapshot{}, Snapshot{ExposuresActive: 3, At: "now"}); d.ExposuresActive != 3 || d.At != "now" {
		t.Fatalf("%+v", d)
	}
}

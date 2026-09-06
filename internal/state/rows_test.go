package state

import (
	"reflect"
	"testing"
)

func port(host string, p int) Port {
	return Port{Host: host, Port: p, BindAddress: "127.0.0.1", PID: p}
}

func TestPrefixKey(t *testing.T) {
	tests := []struct {
		host, key, want string
	}{
		{"", "3000:127.0.0.1", "3000:127.0.0.1"},
		{"localhost", "3000:127.0.0.1", "3000:127.0.0.1"},
		{"hetzner", "3000:127.0.0.1", "hetzner/3000:127.0.0.1"},
	}
	for _, tc := range tests {
		if got := PrefixKey(tc.host, tc.key); got != tc.want {
			t.Errorf("PrefixKey(%q, %q) = %q, want %q", tc.host, tc.key, got, tc.want)
		}
	}
}

func TestTagStampsEveryCollection(t *testing.T) {
	in := Rows{
		Ports:    []Port{port("localhost", 3000)},
		Groups:   []Group{{Name: "api"}},
		Sessions: []SessionRecord{{Session: Session{ID: "s1"}}},
		Tunnels:  []Tunnel{{ID: "t1"}},
		Proxies:  []Proxy{{ID: "p1"}},
		Hosts:    []Host{{Name: LocalhostName, Status: HostConnected}},
	}
	out := in.Tag("hetzner")

	if out.Ports[0].Host != "hetzner" || out.Groups[0].Host != "hetzner" ||
		out.Sessions[0].Host != "hetzner" || out.Tunnels[0].Host != "hetzner" ||
		out.Proxies[0].Host != "hetzner" {
		t.Errorf("Tag left a collection untagged: %+v", out)
	}
	if out.Hosts[0].Name != "hetzner" {
		t.Errorf("host row name = %q, want the registered name", out.Hosts[0].Name)
	}
	if in.Ports[0].Host != "localhost" {
		t.Error("Tag mutated the input rows")
	}

	if want := "hetzner/3000:127.0.0.1"; out.Ports[0].Key() != want {
		t.Errorf("port key = %q, want %q", out.Ports[0].Key(), want)
	}
	if want := "hetzner/api"; out.Groups[0].Key() != want {
		t.Errorf("group key = %q, want %q", out.Groups[0].Key(), want)
	}
	if want := "hetzner/s1"; out.Sessions[0].Key() != want {
		t.Errorf("session key = %q, want %q", out.Sessions[0].Key(), want)
	}
}

func TestParseHostFilter(t *testing.T) {
	tests := []struct {
		name   string
		hosts  []string
		allows map[string]bool
	}{
		{
			name:   "absent means localhost only",
			hosts:  nil,
			allows: map[string]bool{"": true, "localhost": true, "hetzner": false},
		},
		{
			name:   "star means everything",
			hosts:  []string{"*"},
			allows: map[string]bool{"localhost": true, "hetzner": true, "anything": true},
		},
		{
			name:   "a list is exactly that set",
			hosts:  []string{"hetzner"},
			allows: map[string]bool{"localhost": false, "": false, "hetzner": true},
		},
		{
			name:   "localhost can be named alongside a remote",
			hosts:  []string{"localhost", "hetzner"},
			allows: map[string]bool{"localhost": true, "": true, "hetzner": true, "other": false},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := ParseHostFilter(tc.hosts)
			for host, want := range tc.allows {
				if got := f.Allows(host); got != want {
					t.Errorf("Allows(%q) = %v, want %v", host, got, want)
				}
			}
		})
	}
}

func TestHostFilterKeyIsOrderIndependent(t *testing.T) {
	a := ParseHostFilter([]string{"b", "a"})
	b := ParseHostFilter([]string{"a", "b"})
	if a.Key() != b.Key() {
		t.Errorf("keys differ by order: %q vs %q", a.Key(), b.Key())
	}
	if a.Key() == ParseHostFilter([]string{"a"}).Key() {
		t.Error("different sets share a key")
	}
}

func TestFilterSnapshotKeepsOnlyTheAskedHosts(t *testing.T) {
	snap := Snapshot{
		Seq:   7,
		Ports: []Port{port("localhost", 3000), port("hetzner", 3000), port("box", 8080)},
		Hosts: []Host{{Name: "localhost"}, {Name: "hetzner"}, {Name: "box"}},
	}

	local := FilterSnapshot(snap, LocalOnly())
	if len(local.Ports) != 1 || local.Ports[0].Host != "localhost" {
		t.Errorf("localhost-only ports = %+v", local.Ports)
	}
	if len(local.Hosts) != 1 {
		t.Errorf("localhost-only hosts = %+v, want just localhost", local.Hosts)
	}
	if local.Seq != 7 {
		t.Errorf("seq = %d, want the snapshot's own", local.Seq)
	}

	all := FilterSnapshot(snap, AllHosts())
	if len(all.Ports) != 3 {
		t.Errorf(`"*" ports = %+v, want all three`, all.Ports)
	}

	one := FilterSnapshot(snap, ParseHostFilter([]string{"hetzner"}))
	if len(one.Ports) != 1 || one.Ports[0].Host != "hetzner" {
		t.Errorf("hetzner ports = %+v", one.Ports)
	}
}

func TestFilteredCollectionsAreNeverNull(t *testing.T) {
	got := FilterSnapshot(Snapshot{}, LocalOnly())
	if got.Ports == nil || got.Groups == nil || got.Tunnels == nil ||
		got.Proxies == nil || got.Sessions == nil || got.Hosts == nil {
		t.Errorf("filtering left a null collection: %+v", got)
	}
}

func TestApplyIsTheInverseOfDiff(t *testing.T) {
	prev := Snapshot{
		Seq:      1,
		Ports:    []Port{port("localhost", 3000), port("localhost", 8080)},
		Groups:   []Group{{Name: "api", Status: "running"}},
		Sessions: []SessionRecord{{Session: Session{ID: "s1"}}},
		Hosts:    []Host{{Name: "localhost", Status: HostConnected}},
	}
	next := Snapshot{
		Seq:      2,
		At:       "2026-09-06T10:00:00Z",
		Ports:    []Port{port("localhost", 3000), port("localhost", 9000)},
		Groups:   []Group{{Name: "api", Status: "partial"}},
		Sessions: []SessionRecord{},
		Hosts:    []Host{{Name: "localhost", Status: HostConnected}},
	}

	got := Apply(prev, Diff(prev, next))
	if got.Seq != next.Seq || got.At != next.At {
		t.Errorf("seq/at = %d/%q, want %d/%q", got.Seq, got.At, next.Seq, next.At)
	}
	if !sameKeys(got.Ports, next.Ports) {
		t.Errorf("ports = %+v, want %+v", got.Ports, next.Ports)
	}
	if !reflect.DeepEqual(got.Groups, next.Groups) {
		t.Errorf("groups = %+v, want %+v", got.Groups, next.Groups)
	}
	if len(got.Sessions) != 0 {
		t.Errorf("sessions = %+v, want none", got.Sessions)
	}
}

// TestApplyHandlesARestart: Diff emits remove + add for a port whose PID
// changed, and applying them in one delta must leave exactly one row, the new
// one.
func TestApplyHandlesARestart(t *testing.T) {
	old := port("localhost", 3000)
	fresh := old
	fresh.PID = 999

	prev := Snapshot{Seq: 1, Ports: []Port{old}}
	next := Snapshot{Seq: 2, Ports: []Port{fresh}}

	got := Apply(prev, Diff(prev, next))
	if len(got.Ports) != 1 {
		t.Fatalf("ports = %+v, want exactly one", got.Ports)
	}
	if got.Ports[0].PID != 999 {
		t.Errorf("pid = %d, want the restarted process", got.Ports[0].PID)
	}
}

func sameKeys(a, b []Port) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, p := range a {
		seen[p.Key()] = true
	}
	for _, p := range b {
		if !seen[p.Key()] {
			return false
		}
	}
	return true
}

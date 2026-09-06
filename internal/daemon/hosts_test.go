package daemon

import (
	"encoding/json"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/scanner"
	"github.com/raskrebs/sonar/internal/state"
)

func testHost(cpu float64) state.Host {
	return state.Host{
		Name: state.LocalhostName, Address: state.LocalhostName,
		Status: state.HostConnected, CPUPercent: &cpu, Ports: 3, Groups: 1,
	}
}

// `hosts` survives the per-subscriber filter. A machine's load is state the
// daemon collects for itself on every tick, like configured health (contract
// §22), not an enrichment a client opts into with `include`.
func TestFilterSnapshotKeepsHosts(t *testing.T) {
	snap := state.Snapshot{
		Ports: []state.Port{{Port: 3000, Stats: &state.Stats{CPUPercent: 4}}},
		Hosts: []state.Host{testHost(12.5)},
	}
	got := filterSnapshot(snap, scanner.Include{})
	if len(got.Hosts) != 1 || got.Hosts[0].CPUPercent == nil {
		t.Fatalf("hosts = %+v, want the row with its load", got.Hosts)
	}
	if got.Ports[0].Stats != nil {
		t.Error("port stats survived a subscriber that did not ask for them")
	}
}

// Every collection is an array on the wire, never null (contract §5).
func TestFilterSnapshotNeverLeavesHostsNull(t *testing.T) {
	got := filterSnapshot(state.Snapshot{}, scanner.Include{})
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded["hosts"]) != "[]" {
		t.Errorf("hosts = %s, want []", decoded["hosts"])
	}
}

// A tick where only the machine's load moved still reaches a subscriber that
// asked for neither stats nor health.
func TestHostOnlyDeltaIsSentWithoutInclude(t *testing.T) {
	prev := state.Snapshot{Hosts: []state.Host{testHost(12.5)}}
	next := state.Snapshot{Seq: 2, Hosts: []state.Host{testHost(41)}}

	msg := marshalDelta(prev, next, scanner.Include{})
	if msg == nil {
		t.Fatal("a host-only delta was dropped for a subscriber with no include")
	}

	var note struct {
		Method string      `json:"method"`
		Params state.Delta `json:"params"`
	}
	if err := json.Unmarshal(msg, &note); err != nil {
		t.Fatal(err)
	}
	if note.Method != rpc.MethodStateDelta {
		t.Errorf("method = %q, want %q", note.Method, rpc.MethodStateDelta)
	}
	if len(note.Params.Hosts.Updated) != 1 {
		t.Fatalf("hosts change = %+v, want one update", note.Params.Hosts)
	}
	if got := *note.Params.Hosts.Updated[0].CPUPercent; got != 41 {
		t.Errorf("cpu_percent = %v, want 41", got)
	}
}

// A tick where nothing at all moved is still not sent.
func TestEmptyDeltaIsStillDropped(t *testing.T) {
	snap := state.Snapshot{Hosts: []state.Host{testHost(12.5)}}
	if msg := marshalDelta(snap, snap, scanner.Include{}); msg != nil {
		t.Errorf("an empty delta was sent: %s", msg)
	}
}

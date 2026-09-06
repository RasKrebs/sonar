package daemon

import (
	"context"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/state"
)

// TestGroupsListServesTheSnapshotsGroups: `groups.list` is the groups the scan
// already resolved, so the list an agent reads and the groups a subscriber sees
// cannot disagree.
func TestGroupsListServesTheSnapshotsGroups(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newPortsHarness(t, ctx)

	var res rpc.GroupsListResult
	if e := c.call("groups.list", rpc.Empty{}, &res); e != nil {
		t.Fatalf("groups.list: %v", e)
	}
	if res.Groups == nil {
		t.Fatal("groups must always be an array, never null")
	}

	var snapshot struct {
		Groups []state.Group `json:"groups"`
	}
	if e := c.call("state.snapshot", rpc.StateSnapshotParams{}, &snapshot); e != nil {
		t.Fatalf("state.snapshot: %v", e)
	}
	if len(res.Groups) != len(snapshot.Groups) {
		t.Fatalf("groups.list returned %d groups, the snapshot has %d",
			len(res.Groups), len(snapshot.Groups))
	}
	for i := range res.Groups {
		if res.Groups[i].Name != snapshot.Groups[i].Name {
			t.Errorf("group %d = %q, snapshot says %q", i, res.Groups[i].Name, snapshot.Groups[i].Name)
		}
	}
}

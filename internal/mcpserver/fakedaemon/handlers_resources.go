package fakedaemon

import (
	"encoding/json"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/state"
)

// The reads the MCP resources are served from beyond `ports.list` and
// `groups.list`: one group in full, and the sessions collection.

func (f *Fake) registerResourceMethods() {
	f.Handle("groups.inspect", f.handleGroupsInspect)
}

// handleGroupsInspect is the daemon's `groups.inspect`: the group plus the
// ports that belong to it. An unknown name is not_found, which is what makes
// `resources/read sonar://groups/nope` a not_found for the client.
func (f *Fake) handleGroupsInspect(raw json.RawMessage) (any, error) {
	var p rpc.GroupsInspectParams
	if err := unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpc.NewError(rpc.CodeInvalidParams,
			"groups.inspect needs a name", "run `sonar groups` to see the names")
	}

	fx := f.Fixture()
	for _, g := range fx.Groups {
		if g.Name != p.Name {
			continue
		}
		ports := []state.Port{}
		for _, row := range fx.Ports {
			if inGroup(row, g.Name) {
				ports = append(ports, filterPort(row, include{}))
			}
		}
		return rpc.GroupsInspectResult{Group: g, Ports: ports}, nil
	}
	return nil, rpc.NewError(rpc.CodeNotFound,
		"no group named "+p.Name, "run `sonar groups` to see the names")
}

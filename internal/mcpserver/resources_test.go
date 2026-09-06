package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/mcpserver"
	"github.com/raskrebs/sonar/internal/mcpserver/fakedaemon"
	"github.com/raskrebs/sonar/internal/state"
)

// TestResourcesListAdvertisesTheCollections: a client that wants sonar as
// context rather than as a tool finds the four collections and the group
// template, and finds that it may subscribe to them.
func TestResourcesListAdvertisesTheCollections(t *testing.T) {
	h := newHarness(t)

	res, err := h.client.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	byURI := map[string]*mcp.Resource{}
	for _, r := range res.Resources {
		byURI[r.URI] = r
	}
	for _, uri := range []string{mcpserver.URIPorts, mcpserver.URIGroups, mcpserver.URISessions} {
		r, ok := byURI[uri]
		if !ok {
			t.Fatalf("resources/list does not carry %s: %v", uri, byURI)
		}
		if r.MIMEType != mcpserver.ResourceMIME {
			t.Errorf("%s mimeType = %q, want %q", uri, r.MIMEType, mcpserver.ResourceMIME)
		}
		if r.Name == "" || r.Description == "" {
			t.Errorf("%s needs a name and a description a model can act on: %+v", uri, r)
		}
	}

	templates, err := h.client.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatalf("resources/templates/list: %v", err)
	}
	if len(templates.ResourceTemplates) != 1 ||
		templates.ResourceTemplates[0].URITemplate != mcpserver.URIGroupTemplate {
		t.Fatalf("resources/templates/list = %+v, want %s",
			templates.ResourceTemplates, mcpserver.URIGroupTemplate)
	}

	caps := h.client.InitializeResult().Capabilities.Resources
	if caps == nil || !caps.Subscribe || !caps.ListChanged {
		t.Fatalf("resources capabilities = %+v, want subscribe and listChanged", caps)
	}
}

// TestPortsResourceIsPortsListWithoutAppsOrStats pins the body: the daemon's
// own `{ports: [Port]}`, desktop applications filtered out and stats and
// health left uncollected, exactly as spec 2 section 1.2 describes it.
func TestPortsResourceIsPortsListWithoutAppsOrStats(t *testing.T) {
	h := newHarness(t)

	var got rpc.PortsListResult
	readResource(t, h, mcpserver.URIPorts, &got)

	want := []state.Port{}
	for _, p := range fakedaemon.DefaultPorts() {
		if p.IsApp {
			continue
		}
		p.Stats, p.Health = nil, nil
		want = append(want, p)
	}
	if !reflect.DeepEqual(got.Ports, want) {
		t.Fatalf("sonar://ports is not ports.list:\n got %s\nwant %s",
			mustJSON(t, got.Ports), mustJSON(t, want))
	}
}

// TestGroupsResourceIsGroupsList checks the second collection.
func TestGroupsResourceIsGroupsList(t *testing.T) {
	h := newHarness(t)

	var got rpc.GroupsListResult
	readResource(t, h, mcpserver.URIGroups, &got)

	if !reflect.DeepEqual(got.Groups, fakedaemon.DefaultGroups()) {
		t.Fatalf("sonar://groups is not groups.list:\n got %s\nwant %s",
			mustJSON(t, got.Groups), mustJSON(t, fakedaemon.DefaultGroups()))
	}
}

// TestGroupTemplateResolvesToOneGroup is the template half: the name in the
// URI is the name in the daemon call, and the body is groups.inspect —
// the group plus its member ports.
func TestGroupTemplateResolvesToOneGroup(t *testing.T) {
	h := newHarness(t)

	var got rpc.GroupsInspectResult
	readResource(t, h, mcpserver.GroupURI("shop"), &got)

	if got.Name != "shop" {
		t.Fatalf("group name = %q, want shop", got.Name)
	}
	if len(got.Services) != 2 {
		t.Errorf("the group should carry its .sonar.yaml services, got %+v", got.Services)
	}
	ports := []int{}
	for _, p := range got.Ports {
		ports = append(ports, p.Port)
	}
	if !reflect.DeepEqual(ports, []int{3000, 5173}) {
		t.Errorf("member ports = %v, want [3000 5173]", ports)
	}
}

// TestUnknownGroupReadsAsNotFound: a model that guessed a group name gets the
// daemon's own not_found, in the wording the tools use.
func TestUnknownGroupReadsAsNotFound(t *testing.T) {
	h := newHarness(t)

	_, err := h.client.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: mcpserver.GroupURI("nope")})
	if err == nil {
		t.Fatal("reading an unknown group should fail")
	}
	var wire *jsonrpc.Error
	if !errors.As(err, &wire) {
		t.Fatalf("error is %T, want a JSON-RPC error", err)
	}
	if !strings.HasPrefix(wire.Message, "not_found: ") {
		t.Errorf("message = %q, want it to start with not_found:", wire.Message)
	}
	if wire.Code != mcp.CodeResourceNotFound {
		t.Errorf("code = %d, want %d (resource not found)", wire.Code, mcp.CodeResourceNotFound)
	}
	var data struct {
		URI   string `json:"uri"`
		Error struct {
			Code string `json:"code"`
			Hint string `json:"hint"`
		} `json:"error"`
	}
	if err := json.Unmarshal(wire.Data, &data); err != nil {
		t.Fatalf("decoding the error data: %v (%s)", err, wire.Data)
	}
	if data.Error.Code != "not_found" || data.URI != mcpserver.GroupURI("nope") {
		t.Errorf("error data = %+v, want the sonar code and the URI", data)
	}
	if data.Error.Hint == "" {
		t.Error("a domain failure carries a hint")
	}
}

// TestSessionsResourceServesTheCollection: with a daemon that tracks sessions,
// the resource is sessions.list.
func TestSessionsResourceServesTheCollection(t *testing.T) {
	h := newHarnessWith(t, withSessions(fakedaemon.DefaultFixture()))

	var got rpc.SessionsListResult
	readResource(t, h, mcpserver.URISessions, &got)

	if !reflect.DeepEqual(got.Sessions, fakedaemon.DefaultSessions()) {
		t.Fatalf("sonar://sessions is not sessions.list:\n got %s\nwant %s",
			mustJSON(t, got.Sessions), mustJSON(t, fakedaemon.DefaultSessions()))
	}
}

// TestSessionsResourceIsEmptyOnAnOlderDaemon: a daemon that does not advertise
// `sessions` does not make the resource disappear — the resource list is
// advertised once and cached — it makes it an empty list, and the description
// says why.
func TestSessionsResourceIsEmptyOnAnOlderDaemon(t *testing.T) {
	h := newHarness(t) // the default fixture has no sessions capability

	var got rpc.SessionsListResult
	readResource(t, h, mcpserver.URISessions, &got)
	if len(got.Sessions) != 0 {
		t.Fatalf("sessions = %+v, want an empty list", got.Sessions)
	}
	if calls := h.fake.Calls("sessions.list"); calls != 0 {
		t.Errorf("sessions.list was called %d times on a daemon without the capability", calls)
	}

	res, err := h.client.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	for _, r := range res.Resources {
		if r.URI != mcpserver.URISessions {
			continue
		}
		if !strings.Contains(r.Description, "does not track sessions") {
			t.Errorf("the description should say why the list is empty, got %q", r.Description)
		}
		return
	}
	t.Fatal("sonar://sessions is not in resources/list")
}

// withSessions is the fixture of a daemon new enough to have the sessions
// collection (contract §29).
func withSessions(fx fakedaemon.Fixture) fakedaemon.Fixture {
	fx.Capabilities = append(append([]string{}, fx.Capabilities...), "sessions")
	return fx
}

// readResource reads one resource and decodes its JSON body into out.
func readResource(t *testing.T, h *harness, uri string, out any) {
	t.Helper()
	res, err := h.client.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatalf("reading %s: %v", uri, err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("reading %s returned %d contents, want 1", uri, len(res.Contents))
	}
	c := res.Contents[0]
	if c.URI != uri {
		t.Errorf("content URI = %q, want %q", c.URI, uri)
	}
	if c.MIMEType != mcpserver.ResourceMIME {
		t.Errorf("content mimeType = %q, want %q", c.MIMEType, mcpserver.ResourceMIME)
	}
	if !strings.Contains(c.Text, "\n  ") {
		t.Errorf("the body should be indented like `sonar list --json`, got %q", clipText(c.Text))
	}
	if err := json.Unmarshal([]byte(c.Text), out); err != nil {
		t.Fatalf("decoding %s: %v\n%s", uri, err, clipText(c.Text))
	}
}

func clipText(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

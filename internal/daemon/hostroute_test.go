package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// fakeRouter registers a couple of host names and records what was forwarded.
type fakeRouter struct {
	hosts  map[string]bool
	host   string
	method string
	params string
	reply  json.RawMessage
	err    error
}

func (f *fakeRouter) Known(name string) bool { return f.hosts[name] }

func (f *fakeRouter) Forward(_ context.Context, host, method string, params json.RawMessage) (json.RawMessage, RemoteStream, error) {
	f.host, f.method, f.params = host, method, string(params)
	if f.err != nil {
		return nil, nil, f.err
	}
	reply := f.reply
	if reply == nil {
		reply = json.RawMessage(`{"ok":true}`)
	}
	return reply, nil, nil
}

func withRouter(t *testing.T, hosts ...string) *fakeRouter {
	t.Helper()
	r := &fakeRouter{hosts: map[string]bool{}}
	for _, h := range hosts {
		r.hosts[h] = true
	}
	SetRouter(r)
	t.Cleanup(func() { SetRouter(nil) })
	return r
}

func TestRouteRequestDefaultsToLocalhost(t *testing.T) {
	withRouter(t, "hetzner")

	for _, params := range []string{
		`{"port":3000}`,
		`{"host":""}`,
		`{"host":"localhost","port":3000}`,
		`{"targets":[{"port":3000}]}`,
		``,
	} {
		host, _, err := routeRequest("ports.kill", json.RawMessage(params))
		if err != nil {
			t.Fatalf("routeRequest(%q): %v", params, err)
		}
		if host != "" {
			t.Errorf("routeRequest(%q) host = %q, want this machine", params, host)
		}
	}
}

// A request that names no host must reach its handler byte for byte: the
// routing layer is in front of every call, so it has to be invisible when it
// has nothing to do.
func TestRouteRequestLeavesUntouchedParamsAlone(t *testing.T) {
	withRouter(t, "hetzner")
	params := json.RawMessage(`{"targets":[{"port":3000,"bind_address":"127.0.0.1"}],"tree":true}`)
	host, out, err := routeRequest("ports.kill", params)
	if err != nil || host != "" {
		t.Fatalf("routeRequest = (%q, %v)", host, err)
	}
	if string(out) != string(params) {
		t.Errorf("params were rewritten to %s, want %s", out, params)
	}
}

func TestRouteRequestTakesHostOutOfParams(t *testing.T) {
	withRouter(t, "hetzner")

	cases := []struct {
		name   string
		method string
		params string
		want   string
		// wantParams is what the far side must receive: the call, minus what
		// named the host.
		wantParams map[string]any
	}{
		{
			name: "top-level host", method: "ports.kill",
			params: `{"host":"hetzner","targets":[{"port":3000}]}`,
			want:   "hetzner",
			wantParams: map[string]any{
				"targets": []any{map[string]any{"port": 3000.0}},
			},
		},
		{
			name: "host on a target", method: "ports.kill",
			params: `{"targets":[{"host":"hetzner","port":3000}]}`,
			want:   "hetzner",
			wantParams: map[string]any{
				"targets": []any{map[string]any{"port": 3000.0}},
			},
		},
		{
			name: "a delta key as a selector", method: "ports.rename",
			params: `{"key":"hetzner/3000:127.0.0.1","name":"api"}`,
			want:   "hetzner",
			wantParams: map[string]any{
				"port": 3000.0, "bind_address": "127.0.0.1", "name": "api",
			},
		},
		{
			name: "a local delta key", method: "ports.inspect",
			params:     `{"key":"3000:::1"}`,
			want:       "",
			wantParams: map[string]any{"port": 3000.0, "bind_address": "::1"},
		},
		{
			name: "a bare port key", method: "ports.inspect",
			params:     `{"key":"3000"}`,
			want:       "",
			wantParams: map[string]any{"port": 3000.0},
		},
		{
			name: "a prefixed group name", method: "groups.kill",
			params:     `{"name":"hetzner/api"}`,
			want:       "hetzner",
			wantParams: map[string]any{"name": "api"},
		},
		{
			name: "a prefixed session id", method: "sessions.kill",
			params:     `{"id":"hetzner/sess-1"}`,
			want:       "hetzner",
			wantParams: map[string]any{"id": "sess-1"},
		},
		{
			// ports.rename's `name` is the new display name, never a key.
			name: "rename keeps its value", method: "ports.rename",
			params:     `{"host":"hetzner","port":3000,"name":"hetzner/api"}`,
			want:       "hetzner",
			wantParams: map[string]any{"port": 3000.0, "name": "hetzner/api"},
		},
		{
			// A group really called "staging/api" on a host that is not
			// registered stays whole.
			name: "an unregistered prefix is part of the name", method: "groups.kill",
			params:     `{"name":"staging/api"}`,
			want:       "",
			wantParams: map[string]any{"name": "staging/api"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host, out, err := routeRequest(c.method, json.RawMessage(c.params))
			if err != nil {
				t.Fatalf("routeRequest: %v", err)
			}
			if host != c.want {
				t.Errorf("host = %q, want %q", host, c.want)
			}
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("params %s: %v", out, err)
			}
			if !jsonEqual(got, c.wantParams) {
				t.Errorf("params = %v, want %v", got, c.wantParams)
			}
		})
	}
}

func TestRouteRequestRefusesTwoHosts(t *testing.T) {
	withRouter(t, "hetzner", "berlin")
	_, _, err := routeRequest("ports.kill",
		json.RawMessage(`{"targets":[{"host":"hetzner","port":3000},{"host":"berlin","port":3001}]}`))
	if err == nil {
		t.Fatal("want an error when one call names two hosts")
	}
	var re *rpc.Error
	if !errors.As(err, &re) || re.Data.Code != "invalid_params" {
		t.Fatalf("err = %v, want invalid_params", err)
	}
	if !strings.Contains(err.Error(), "hetzner") || !strings.Contains(err.Error(), "berlin") {
		t.Errorf("error = %v, want it to name both hosts", err)
	}
}

// The same host named twice is one host, not a conflict.
func TestRouteRequestAcceptsOneHostNamedTwice(t *testing.T) {
	withRouter(t, "hetzner")
	host, _, err := routeRequest("ports.kill",
		json.RawMessage(`{"host":"hetzner","targets":[{"key":"hetzner/3000"},{"port":3001}]}`))
	if err != nil {
		t.Fatalf("routeRequest: %v", err)
	}
	if host != "hetzner" {
		t.Errorf("host = %q, want hetzner", host)
	}
}

func TestRouteRequestRefusesAKeyBesideAPort(t *testing.T) {
	withRouter(t, "hetzner")
	_, _, err := routeRequest("ports.inspect", json.RawMessage(`{"key":"3000","port":3001}`))
	if err == nil {
		t.Fatal("want an error for a selector that sets both")
	}
	var re *rpc.Error
	if !errors.As(err, &re) || re.Data.Code != "invalid_selector" {
		t.Fatalf("err = %v, want invalid_selector", err)
	}
}

func TestRouteRequestRefusesAnUnreadableKey(t *testing.T) {
	withRouter(t, "hetzner")
	_, _, err := routeRequest("ports.inspect", json.RawMessage(`{"key":"hetzner/not-a-port"}`))
	if err == nil {
		t.Fatal("want an error for a key that is not a port key")
	}
}

// state.* has its own host selection and must not be caught by this one.
func TestRouteRequestLeavesStateAndRemoteAlone(t *testing.T) {
	withRouter(t, "hetzner")
	for _, method := range []string{
		"state.subscribe", "state.snapshot", "stream.cancel",
		"daemon.hello", "daemon.shutdown", "remote.call", "remote.add",
	} {
		params := json.RawMessage(`{"host":"hetzner"}`)
		host, out, err := routeRequest(method, params)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if host != "" {
			t.Errorf("%s routed to %q, want it left alone", method, host)
		}
		if string(out) != string(params) {
			t.Errorf("%s params = %s, want them untouched", method, out)
		}
	}
}

func TestForwardToUnknownHostWithoutARouter(t *testing.T) {
	SetRouter(nil)
	_, err := ForwardTo(context.Background(), &Request{}, "hetzner", "ports.list", nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Data.Code != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestForwardToAnUnregisteredHostIsNotFound(t *testing.T) {
	withRouter(t, "hetzner")
	_, err := ForwardTo(context.Background(), &Request{}, "berlin", "ports.list", nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Data.Code != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
	if !strings.Contains(err.Error(), "berlin") {
		t.Errorf("err = %v, want it to name the host", err)
	}
}

func TestForwardToReturnsTheRemoteResult(t *testing.T) {
	r := withRouter(t, "hetzner")
	r.reply = json.RawMessage(`{"ports":[{"host":"localhost","port":3000,"pid":8123456}]}`)

	out, err := ForwardTo(context.Background(), &Request{}, "hetzner", "ports.list",
		json.RawMessage(`{"all":true}`))
	if err != nil {
		t.Fatalf("ForwardTo: %v", err)
	}
	if r.host != "hetzner" || r.method != "ports.list" || r.params != `{"all":true}` {
		t.Errorf("forwarded (%q, %q, %s)", r.host, r.method, r.params)
	}

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"host":"hetzner"`) {
		t.Errorf("result = %s, want the row retagged to the host it came from", raw)
	}
	// A pid larger than float64's exact range must survive the retagging.
	if !strings.Contains(string(raw), `8123456`) {
		t.Errorf("result = %s, want the pid unchanged", raw)
	}
}

func TestRetagResultNamespacesPortKeys(t *testing.T) {
	raw := retagResult("ports.kill", "hetzner", json.RawMessage(
		`{"ok":true,"affected":["3000:127.0.0.1"],"results":[{"host":"localhost","port":3000}]}`))

	var got struct {
		Affected []string `json:"affected"`
		Results  []struct {
			Host string `json:"host"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Affected) != 1 || got.Affected[0] != "hetzner/3000:127.0.0.1" {
		t.Errorf("affected = %v, want the key the stream uses", got.Affected)
	}
	if len(got.Results) != 1 || got.Results[0].Host != "hetzner" {
		t.Errorf("results = %+v, want the row tagged with the host", got.Results)
	}
}

// A method whose `key` is not a port key keeps it: a claim key is not a row.
func TestRetagResultLeavesNonPortKeysAlone(t *testing.T) {
	raw := retagResult("claims.release", "hetzner", json.RawMessage(`{"ok":true,"key":"my-project"}`))
	if !strings.Contains(string(raw), `"my-project"`) {
		t.Errorf("result = %s, want the claim key untouched", raw)
	}
}

func TestRetagResultPassesThroughWhatItCannotParse(t *testing.T) {
	raw := retagResult("ports.list", "hetzner", json.RawMessage(`not json`))
	if string(raw) != "not json" {
		t.Errorf("result = %s, want it passed through", raw)
	}
}

func jsonEqual(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

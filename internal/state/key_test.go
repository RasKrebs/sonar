package state

import "testing"

func TestSplitHostPrefix(t *testing.T) {
	known := func(name string) bool { return name == "hetzner" }

	cases := []struct {
		key       string
		wantHost  string
		wantRest  string
		knownFunc func(string) bool
	}{
		{"3000:127.0.0.1", "", "3000:127.0.0.1", known},
		{"hetzner/3000:127.0.0.1", "hetzner", "3000:127.0.0.1", known},
		{"localhost/3000:127.0.0.1", "localhost", "3000:127.0.0.1", known},
		// An unregistered prefix is not a host: it is part of the name, and a
		// group may legitimately be called that.
		{"nope/3000", "", "nope/3000", known},
		{"my/group", "", "my/group", known},
		{"hetzner/api", "hetzner", "api", known},
		// Nothing is known when there are no hosts at all.
		{"hetzner/3000", "", "hetzner/3000", nil},
		// Degenerate forms stay whole.
		{"/3000", "", "/3000", known},
		{"hetzner/", "", "hetzner/", known},
		{"", "", "", known},
	}
	for _, c := range cases {
		host, rest := SplitHostPrefix(c.key, c.knownFunc)
		if host != c.wantHost || rest != c.wantRest {
			t.Errorf("SplitHostPrefix(%q) = (%q, %q), want (%q, %q)",
				c.key, host, rest, c.wantHost, c.wantRest)
		}
	}
}

func TestParsePortKey(t *testing.T) {
	cases := []struct {
		key      string
		wantPort int
		wantBind string
		wantOK   bool
	}{
		{"3000", 3000, "", true},
		{"3000:127.0.0.1", 3000, "127.0.0.1", true},
		{"3000:0.0.0.0", 3000, "0.0.0.0", true},
		// An IPv6 bind address is full of colons; only the first one splits.
		{"8080:::1", 8080, "::1", true},
		{"8080:::", 8080, "::", true},
		{"8080:*", 8080, "*", true},
		{"", 0, "", false},
		{"api", 0, "", false},
		{"0:127.0.0.1", 0, "", false},
		{"70000", 0, "", false},
	}
	for _, c := range cases {
		port, bind, ok := ParsePortKey(c.key)
		if port != c.wantPort || bind != c.wantBind || ok != c.wantOK {
			t.Errorf("ParsePortKey(%q) = (%d, %q, %v), want (%d, %q, %v)",
				c.key, port, bind, ok, c.wantPort, c.wantBind, c.wantOK)
		}
	}
}

// The two halves must compose: what PrefixKey builds, SplitHostPrefix and
// ParsePortKey take apart again.
func TestKeyRoundTrip(t *testing.T) {
	known := func(name string) bool { return name == "hetzner" }
	for _, host := range []string{"", LocalhostName, "hetzner"} {
		p := Port{Host: host, Port: 3000, BindAddress: "::1"}
		gotHost, rest := SplitHostPrefix(p.Key(), known)
		if HostOf(gotHost) != HostOf(host) {
			t.Errorf("host of %q = %q, want %q", p.Key(), gotHost, host)
		}
		port, bind, ok := ParsePortKey(rest)
		if !ok || port != 3000 || bind != "::1" {
			t.Errorf("ParsePortKey(%q) = (%d, %q, %v)", rest, port, bind, ok)
		}
	}
}

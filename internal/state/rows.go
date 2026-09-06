package state

import "sort"

// Rows is one machine's contribution to the published collections: everything
// a Snapshot carries except the sequence number and the counters, which belong
// to the stream rather than to a host.
//
// The local daemon builds one Rows from its own scan and one per connected
// remote bridge, then concatenates them into the Snapshot every subscriber
// reads. Keeping the per-host pieces separate is what lets a remote host's
// state change publish a delta without re-scanning the local machine, and lets
// a host's rows be dropped wholesale when its bridge goes away.
type Rows struct {
	Ports    []Port
	Groups   []Group
	Tunnels  []Tunnel
	Proxies  []Proxy
	Sessions []SessionRecord
	Hosts    []Host
}

// RowsOf takes the collections out of a snapshot.
func RowsOf(s Snapshot) Rows {
	return Rows{
		Ports:    s.Ports,
		Groups:   s.Groups,
		Tunnels:  s.Tunnels,
		Proxies:  s.Proxies,
		Sessions: s.Sessions,
		Hosts:    s.Hosts,
	}
}

// Tag stamps every row with a host name and returns the result. It is how a
// remote host's own state — which calls itself "localhost", because from that
// daemon's point of view it is — becomes rows the local stream can carry
// alongside its own without a key collision.
//
// The `hosts` collection is tagged too: the remote's localhost row is renamed
// to the registered name, which is what the spec means by "the local daemon
// copies each remote's hosts[0] into its own hosts".
func (r Rows) Tag(host string) Rows {
	out := Rows{
		Ports:    make([]Port, len(r.Ports)),
		Groups:   make([]Group, len(r.Groups)),
		Tunnels:  make([]Tunnel, len(r.Tunnels)),
		Proxies:  make([]Proxy, len(r.Proxies)),
		Sessions: make([]SessionRecord, len(r.Sessions)),
		Hosts:    make([]Host, len(r.Hosts)),
	}
	copy(out.Ports, r.Ports)
	copy(out.Groups, r.Groups)
	copy(out.Tunnels, r.Tunnels)
	copy(out.Proxies, r.Proxies)
	copy(out.Sessions, r.Sessions)
	copy(out.Hosts, r.Hosts)

	for i := range out.Ports {
		out.Ports[i].Host = host
	}
	for i := range out.Groups {
		out.Groups[i].Host = host
	}
	for i := range out.Tunnels {
		out.Tunnels[i].Host = host
	}
	for i := range out.Proxies {
		out.Proxies[i].Host = host
	}
	for i := range out.Sessions {
		out.Sessions[i].Host = host
	}
	for i := range out.Hosts {
		out.Hosts[i].Name = host
	}
	return out
}

// Append concatenates other onto r.
func (r Rows) Append(other Rows) Rows {
	r.Ports = append(r.Ports, other.Ports...)
	r.Groups = append(r.Groups, other.Groups...)
	r.Tunnels = append(r.Tunnels, other.Tunnels...)
	r.Proxies = append(r.Proxies, other.Proxies...)
	r.Sessions = append(r.Sessions, other.Sessions...)
	r.Hosts = append(r.Hosts, other.Hosts...)
	return r
}

// Normalize replaces every nil collection with an empty one, so the published
// JSON never carries a null where the contract promises an array.
func (r Rows) Normalize() Rows {
	if r.Ports == nil {
		r.Ports = []Port{}
	}
	if r.Groups == nil {
		r.Groups = []Group{}
	}
	if r.Tunnels == nil {
		r.Tunnels = []Tunnel{}
	}
	if r.Proxies == nil {
		r.Proxies = []Proxy{}
	}
	if r.Sessions == nil {
		r.Sessions = []SessionRecord{}
	}
	if r.Hosts == nil {
		r.Hosts = []Host{}
	}
	return r
}

// Into copies the collections onto a snapshot, leaving seq, timestamp and the
// counters alone.
func (r Rows) Into(s Snapshot) Snapshot {
	r = r.Normalize()
	s.Ports, s.Groups, s.Tunnels = r.Ports, r.Groups, r.Tunnels
	s.Proxies, s.Sessions, s.Hosts = r.Proxies, r.Sessions, r.Hosts
	return s
}

// HostFilter decides which hosts' rows a subscriber sees. The zero value is
// what `state.subscribe` with no `hosts` means: localhost only, which is
// exactly the stream every pre-3A.2 client already reads.
type HostFilter struct {
	all  bool
	set  map[string]bool
	name string
}

// LocalOnly is the default filter.
func LocalOnly() HostFilter { return HostFilter{name: LocalhostName} }

// AllHosts matches every host, registered or not.
func AllHosts() HostFilter { return HostFilter{all: true, name: "*"} }

// ParseHostFilter turns the wire's `hosts` list into a filter. An absent or
// empty list is localhost only; a list containing "*" is every host; anything
// else is exactly the named set, so a client that wants localhost alongside a
// remote host names both.
func ParseHostFilter(hosts []string) HostFilter {
	if len(hosts) == 0 {
		return LocalOnly()
	}
	set := map[string]bool{}
	for _, h := range hosts {
		if h == "*" {
			return AllHosts()
		}
		if h == "" {
			h = LocalhostName
		}
		set[h] = true
	}
	names := make([]string, 0, len(set))
	for h := range set {
		names = append(names, h)
	}
	sort.Strings(names)
	name := ""
	for i, h := range names {
		if i > 0 {
			name += "\x00"
		}
		name += h
	}
	return HostFilter{set: set, name: name}
}

// Allows reports whether a row tagged with this host is visible.
func (f HostFilter) Allows(host string) bool {
	if f.all {
		return true
	}
	if f.set == nil {
		return IsLocalhost(host)
	}
	return f.set[HostOf(host)]
}

// All reports whether the filter is "*".
func (f HostFilter) All() bool { return f.all }

// Key is a comparable identity for the filter, so the daemon can marshal one
// delta per distinct filter rather than one per subscriber.
func (f HostFilter) Key() string { return f.name }

// Filter keeps only the rows this filter allows.
func (r Rows) Filter(f HostFilter) Rows {
	if f.All() {
		return r
	}
	out := Rows{}
	for _, p := range r.Ports {
		if f.Allows(p.Host) {
			out.Ports = append(out.Ports, p)
		}
	}
	for _, g := range r.Groups {
		if f.Allows(g.Host) {
			out.Groups = append(out.Groups, g)
		}
	}
	for _, t := range r.Tunnels {
		if f.Allows(t.Host) {
			out.Tunnels = append(out.Tunnels, t)
		}
	}
	for _, p := range r.Proxies {
		if f.Allows(p.Host) {
			out.Proxies = append(out.Proxies, p)
		}
	}
	for _, s := range r.Sessions {
		if f.Allows(s.Host) {
			out.Sessions = append(out.Sessions, s)
		}
	}
	for _, h := range r.Hosts {
		if f.Allows(h.Name) {
			out.Hosts = append(out.Hosts, h)
		}
	}
	return out.Normalize()
}

// FilterSnapshot keeps only the rows f allows, leaving seq and the counters
// untouched.
func FilterSnapshot(s Snapshot, f HostFilter) Snapshot {
	if f.All() {
		return s
	}
	return RowsOf(s).Filter(f).Into(s)
}

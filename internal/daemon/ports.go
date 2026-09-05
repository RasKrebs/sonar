package daemon

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/docker"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/scanner"
	"github.com/raskrebs/sonar/internal/state"
)

// The read half of the ports namespace. Every handler here answers from the
// scanner's shared snapshot rather than scanning for itself, so N clients
// reading at once cost the machine one scan, not N. See readPorts.
func init() {
	RegisterHandler("ports.list", handlePortsList)
	RegisterHandler("ports.inspect", handlePortsInspect)
	RegisterHandler("ports.next", handlePortsNext)
	RegisterHandler("ports.health", handlePortsHealth)
	RegisterHandler("ports.graph", handlePortsGraph)
	RegisterHandler("ports.logs", handlePortsLogs)
	RegisterHandler("ports.wait", handlePortsWait)
}

// readPorts serves the rows every read handler starts from.
//
// While subscribers are connected the scan loop is already running, so the
// cached snapshot is never more than one scan interval old and a plain read is
// answered from it. That is what keeps the scan counter flat: a `sonar list`
// next to a running `sonar watch` costs nothing, and ten of them cost nothing
// either. A read that asks for stats or health cannot use the cache unless a
// subscriber is already collecting them, and a read with no loop behind it
// scans for itself (reusing a scan younger than scanner.CacheTTL).
func readPorts(rt *Runtime, include scanner.Include) ([]state.Port, error) {
	if rt.Subscribers() > 0 {
		if snap := rt.Scanner.Cached(); snap.Seq > 0 && cacheCovers(snap, include) {
			return snap.Ports, nil
		}
	}
	snap, err := rt.Scanner.Snapshot(include)
	if err != nil {
		return nil, rpc.NewError(rpc.CodeInternal, "scan failed: "+err.Error(),
			"check `sonar daemon log` for the scanner error")
	}
	return snap.Ports, nil
}

// cacheCovers reports whether the cached snapshot already carries the
// enrichments this read asked for. An empty port table trivially covers
// everything: there is nothing to enrich.
func cacheCovers(snap state.Snapshot, include scanner.Include) bool {
	if !include.Stats && !include.Health {
		return true
	}
	for i := range snap.Ports {
		if include.Stats && snap.Ports[i].Stats == nil {
			return false
		}
		if include.Health && snap.Ports[i].Health == nil {
			return false
		}
	}
	return true
}

func handlePortsList(_ context.Context, req *Request) (any, error) {
	var p rpc.PortsListParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	include, err := parseInclude(p.Include)
	if err != nil {
		return nil, err
	}
	if p.Filter != nil {
		switch *p.Filter {
		case "", "docker", "user", "system", "proxy":
		default:
			return nil, rpc.NewError(rpc.CodeInvalidParams,
				"unknown filter "+*p.Filter, `filter accepts "docker", "user", "system" and "proxy"`)
		}
	}
	ipVersion, err := normalizeIPVersion(p.IPVersion)
	if err != nil {
		return nil, err
	}

	rows, err := readPorts(req.Runtime, include)
	if err != nil {
		return nil, err
	}

	out := make([]state.Port, 0, len(rows))
	for _, row := range rows {
		if !p.All && row.IsApp {
			continue
		}
		if p.Filter != nil && *p.Filter != "" && string(row.Type) != *p.Filter {
			continue
		}
		if ipVersion != "" && row.IPVersion != ipVersion {
			continue
		}
		if p.Group != nil && *p.Group != "" && !inGroup(row, *p.Group) {
			continue
		}
		out = append(out, filterPort(row, include))
	}
	return rpc.PortsListResult{Ports: out}, nil
}

// inGroup matches the CLI's own group filter: a group name, a `sonar start`
// name or a run id all select the run's ports.
func inGroup(p state.Port, group string) bool {
	if p.Group != nil && *p.Group == group {
		return true
	}
	if p.Run != nil && (p.Run.Name == group || p.Run.ID == group || p.Run.Group == group) {
		return true
	}
	return false
}

// normalizeIPVersion accepts both the wire's "IPv4"/"IPv6" and the shorthand
// the CLI's -4/-6 flags speak.
func normalizeIPVersion(v *string) (string, error) {
	if v == nil {
		return "", nil
	}
	switch strings.ToLower(strings.TrimSpace(*v)) {
	case "":
		return "", nil
	case "4", "ipv4":
		return "IPv4", nil
	case "6", "ipv6":
		return "IPv6", nil
	default:
		return "", rpc.NewError(rpc.CodeInvalidParams,
			"unknown ip_version "+*v, `ip_version accepts "IPv4" and "IPv6"`)
	}
}

// resolvePort finds the one row a selector addresses. A port bound to several
// addresses without a bind_address is ambiguous (1002), not a silent pick.
func resolvePort(rows []state.Port, sel rpc.Selector) (state.Port, error) {
	var matches []state.Port
	switch {
	case sel.PID != nil:
		for _, row := range rows {
			if row.PID == *sel.PID {
				matches = append(matches, row)
			}
		}
		if len(matches) == 0 {
			return state.Port{}, rpc.NewError(rpc.CodeNotFound,
				fmt.Sprintf("no listening port found for pid %d", *sel.PID),
				"run `sonar list` to see what is listening")
		}
		return matches[0], nil
	case sel.Port != nil:
		for _, row := range rows {
			if row.Port != *sel.Port {
				continue
			}
			if sel.BindAddress != nil && *sel.BindAddress != "" && row.BindAddress != *sel.BindAddress {
				continue
			}
			matches = append(matches, row)
		}
	default:
		return state.Port{}, rpc.NewError(rpc.CodeInvalidParams,
			"a selector needs a port or a pid", `send {"port": 3000} or {"pid": 1234}`)
	}

	switch len(matches) {
	case 0:
		if sel.BindAddress != nil && *sel.BindAddress != "" {
			return state.Port{}, rpc.NewError(rpc.CodeNotFound,
				fmt.Sprintf("no process found listening on %s:%d", *sel.BindAddress, *sel.Port), "")
		}
		return state.Port{}, rpc.NewError(rpc.CodeNotFound,
			fmt.Sprintf("no process found listening on port %d", *sel.Port),
			"run `sonar list` to see what is listening")
	case 1:
		return matches[0], nil
	default:
		addrs := make([]string, 0, len(matches))
		for _, m := range matches {
			addrs = append(addrs, m.BindAddress)
		}
		return state.Port{}, rpc.NewError(rpc.CodeAmbiguous,
			fmt.Sprintf("port %d is bound to multiple addresses: %s", *sel.Port, strings.Join(addrs, ", ")),
			fmt.Sprintf("pass a bind address (e.g. --ip %s)", addrs[0]))
	}
}

func handlePortsInspect(_ context.Context, req *Request) (any, error) {
	var sel rpc.Selector
	if err := req.Bind(&sel); err != nil {
		return nil, err
	}

	// Stats come off the shared scan; health is probed for this one port only,
	// the way `sonar info` has always done it.
	rows, err := readPorts(req.Runtime, scanner.Include{Stats: true})
	if err != nil {
		return nil, err
	}
	row, err := resolvePort(rows, sel)
	if err != nil {
		return nil, err
	}

	probe := ports.ProbeHealth(row.BindAddress, row.Port, "/", scanner.HealthTimeout)
	row.Health = &state.Health{
		Status:    probe.Status,
		Code:      probe.StatusCode,
		LatencyMs: probe.Latency.Milliseconds(),
	}

	sources := []string{}
	for _, s := range ports.FindLogSources(row.PID) {
		if s.FD != "" {
			sources = append(sources, fmt.Sprintf("%s (%s)", s.Path, s.FD))
			continue
		}
		sources = append(sources, s.Path)
	}

	return rpc.PortsInspectResult{
		Port:        row,
		LogSources:  sources,
		Connections: peersOf(row, rows),
	}, nil
}

// peersOf lists the listening ports this one is connected to. It reuses the
// same established-connection scan `sonar graph` runs.
func peersOf(row state.Port, rows []state.Port) []rpc.Connection {
	edges, err := ports.BuildGraph(state.ToListeningAll(rows))
	if err != nil {
		return []rpc.Connection{}
	}
	pidOf := map[int]int{}
	for _, r := range rows {
		if _, seen := pidOf[r.Port]; !seen {
			pidOf[r.Port] = r.PID
		}
	}
	out := []rpc.Connection{}
	for _, e := range edges {
		switch row.Port {
		case e.FromPort:
			out = append(out, rpc.Connection{RemotePort: e.ToPort, RemotePID: pidOf[e.ToPort]})
		case e.ToPort:
			out = append(out, rpc.Connection{RemotePort: e.FromPort, RemotePID: pidOf[e.FromPort]})
		}
	}
	return out
}

func handlePortsNext(_ context.Context, req *Request) (any, error) {
	var p rpc.PortsNextParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	start, end, count := p.Start, p.End, p.Count
	if start == 0 {
		start = 3000
	}
	if end == 0 {
		end = 65535
	}
	if count == 0 {
		count = 1
	}
	if start < 1 || end > 65535 || start > end {
		return nil, rpc.NewError(rpc.CodeInvalidParams,
			fmt.Sprintf("invalid port range %d-%d", start, end), "")
	}
	if count < 1 {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "count must be at least 1", "")
	}

	rows, err := readPorts(req.Runtime, scanner.Include{})
	if err != nil {
		return nil, err
	}
	occupied := make(map[int]bool, len(rows))
	for _, row := range rows {
		occupied[row.Port] = true
	}

	// Claims (spec 2) exclude further ports here in a later step.
	free := make([]int, 0, count)
	for port := start; port <= end; port++ {
		if occupied[port] {
			free = free[:0]
			continue
		}
		free = append(free, port)
		if len(free) == count {
			return rpc.PortsNextResult{Ports: free}, nil
		}
	}
	return nil, rpc.NewError(rpc.CodeNotFound,
		fmt.Sprintf("no %d consecutive free port(s) in range %d-%d", count, start, end),
		"widen the range")
}

func handlePortsHealth(_ context.Context, req *Request) (any, error) {
	var p rpc.PortsHealthParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	rows, err := readPorts(req.Runtime, scanner.Include{})
	if err != nil {
		return nil, err
	}

	targets := make([]state.Port, 0, len(rows))
	if len(p.Ports) == 0 {
		targets = append(targets, rows...)
	} else {
		byPort := map[int]state.Port{}
		for _, row := range rows {
			if _, seen := byPort[row.Port]; !seen {
				byPort[row.Port] = row
			}
		}
		for _, want := range p.Ports {
			if row, ok := byPort[want]; ok {
				targets = append(targets, row)
				continue
			}
			// A port nobody is listening on still gets a row, so a caller
			// asking about a fixed list never has to match up indexes.
			targets = append(targets, state.Port{Port: want})
		}
	}

	results := make([]rpc.PortHealth, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10)
	for i := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			r := ports.ProbeHealth(targets[i].BindAddress, targets[i].Port, "/", scanner.HealthTimeout)
			results[i] = rpc.PortHealth{
				Port:      targets[i].Port,
				Status:    r.Status,
				Code:      r.StatusCode,
				LatencyMs: r.Latency.Milliseconds(),
			}
		}(i)
	}
	wg.Wait()
	return rpc.PortsHealthResult{Results: results}, nil
}

func handlePortsGraph(_ context.Context, req *Request) (any, error) {
	rows, err := readPorts(req.Runtime, scanner.Include{})
	if err != nil {
		return nil, err
	}
	listening := state.ToListeningAll(rows)

	edges, err := ports.BuildGraph(listening)
	if err != nil {
		return nil, rpc.NewError(rpc.CodeInternal, "building the connection graph: "+err.Error(), "")
	}
	containerEdges, err := docker.BuildDockerGraph(listening)
	if err != nil {
		return nil, rpc.NewError(rpc.CodeInternal, "building the container graph: "+err.Error(), "")
	}
	edges = append(edges, containerEdges...)

	pidOf := map[int]int{}
	for _, r := range rows {
		if _, seen := pidOf[r.Port]; !seen {
			pidOf[r.Port] = r.PID
		}
	}
	out := make([]rpc.GraphEdge, 0, len(edges))
	for _, e := range edges {
		out = append(out, rpc.GraphEdge{
			FromPort:    e.FromPort,
			FromPID:     pidOf[e.FromPort],
			FromProcess: e.FromProcess,
			ToPort:      e.ToPort,
			ToPID:       pidOf[e.ToPort],
			ToProcess:   e.ToProcess,
		})
	}
	return rpc.PortsGraphResult{Connections: out}, nil
}

// sortedPorts is a small helper the tests and the wait handler share.
func sortedPorts(pp []int) []int {
	out := append([]int{}, pp...)
	sort.Ints(out)
	return out
}

// nowRFC3339 stamps stream chunks.
func nowRFC3339() string { return time.Now().Format(time.RFC3339) }

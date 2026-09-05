package groups

import (
	"sort"

	"github.com/raskrebs/sonar/internal/state"
)

// Groups builds the group collection from resolved ports. Every group that at
// least one port resolves to appears, plus every valid `.sonar.yaml` the index
// knows about — a project whose services are all down is still a group, it is
// just stopped.
//
// index may be nil, in which case groups carry no services and no config path.
func Groups(pp []state.Port, index *Index) []state.Group {
	if index == nil {
		index = NewIndex()
	}
	byName := map[string]*state.Group{}
	members := map[string][]state.Port{}

	order := func(name string) *state.Group {
		g, ok := byName[name]
		if !ok {
			g = &state.Group{Name: name, Source: state.SourceAuto, Members: []int{}, Services: []state.Service{}}
			byName[name] = g
		}
		return g
	}

	for _, p := range pp {
		if p.Group == nil || *p.Group == "" {
			continue
		}
		g := order(*p.Group)
		if p.GroupSource != nil && rank(*p.GroupSource) > rank(g.Source) {
			g.Source = *p.GroupSource
		}
		if !containsInt(g.Members, p.Port) {
			g.Members = append(g.Members, p.Port)
		}
		if g.RootDir == nil && p.ProjectRoot != nil {
			root := *p.ProjectRoot
			g.RootDir = &root
		}
		members[*p.Group] = append(members[*p.Group], p)
	}

	for _, cfg := range index.Configs() {
		g := order(cfg.Name)
		path, dir := cfg.Path, cfg.Dir
		g.ConfigPath = &path
		g.RootDir = &dir
		if rank(state.SourceFile) > rank(g.Source) {
			g.Source = state.SourceFile
		}
		g.Services = services(cfg, members[cfg.Name])
	}

	out := make([]state.Group, 0, len(byName))
	for _, g := range byName {
		sort.Ints(g.Members)
		g.Status = status(*g)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// services joins a config's declared services against the ports actually
// listening in that group: by declared port first, then by the name the
// scanner shows for the port.
func services(cfg *Config, member []state.Port) []state.Service {
	out := make([]state.Service, 0, len(cfg.Services))
	for _, s := range cfg.Services {
		svc := state.Service{
			Name:      s.Name,
			Cmd:       s.Cmd,
			Cwd:       s.Cwd,
			DependsOn: append([]string{}, s.DependsOn...),
		}
		if svc.DependsOn == nil {
			svc.DependsOn = []string{}
		}
		if s.Port != 0 {
			port := s.Port
			svc.Port = &port
		}
		if s.Health != "" {
			health := s.Health
			svc.Health = &health
		}
		for _, p := range member {
			if (s.Port != 0 && p.Port == s.Port) || p.DisplayName == s.Name ||
				(p.Run != nil && p.Run.Name == s.Name) {
				actual := p.Port
				svc.Running, svc.PortActual = true, &actual
				break
			}
		}
		out = append(out, svc)
	}
	return out
}

// status is running when everything the group declares is up, stopped when
// nothing is, and partial in between. A group with no services is running as
// long as it has a listening port.
func status(g state.Group) string {
	if len(g.Services) == 0 {
		if len(g.Members) > 0 {
			return "running"
		}
		return "stopped"
	}
	up := 0
	for _, s := range g.Services {
		if s.Running {
			up++
		}
	}
	switch {
	case up == len(g.Services):
		return "running"
	case up == 0:
		if len(g.Members) > 0 {
			return "partial"
		}
		return "stopped"
	default:
		return "partial"
	}
}

// rank orders group sources by precedence so a group takes the strongest
// source any of its members resolved with.
func rank(s state.GroupSource) int {
	switch s {
	case state.SourceManual:
		return 3
	case state.SourceStart:
		return 2
	case state.SourceFile:
		return 1
	default:
		return 0
	}
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

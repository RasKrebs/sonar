package groups

import (
	"os"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// Attribute runs the resolver over a direct scan. It builds an index from what
// the scan saw — every `.sonar.yaml` above a process cwd, every Compose
// project's working directory, plus the config in the caller's own working
// directory — resolves the group of every port, writes Group, GroupSource and
// ProjectRoot back onto the scanner rows, and returns the resolved contract
// rows together with the index.
//
// This is the no-daemon path: `sonar list`, `sonar groups` and `sonar init`
// call it. The daemon's scanner keeps a long-lived index and calls Resolve
// directly.
func Attribute(pp []ports.ListeningPort) ([]state.Port, *Index) {
	index := NewIndex()
	if wd, err := os.Getwd(); err == nil {
		index.Observe(wd)
	}
	for i := range pp {
		index.AddComposeProject(pp[i].DockerComposeProject, pp[i].DockerComposeWorkingDir)
	}
	for i := range pp {
		index.Observe(pp[i].Cwd)
	}
	for i := range pp {
		if dir := index.ComposeDir(pp[i].DockerComposeProject); dir != "" {
			index.Observe(dir)
		}
	}

	resolved := Resolve(state.FromListeningAll(pp), NoPins{}, PortRuns{}, index)
	for i := range resolved {
		pp[i].ProjectRoot = deref(resolved[i].ProjectRoot)
		pp[i].Group = deref(resolved[i].Group)
		pp[i].GroupSource = ""
		if resolved[i].GroupSource != nil {
			pp[i].GroupSource = string(*resolved[i].GroupSource)
		}
	}
	return resolved, index
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

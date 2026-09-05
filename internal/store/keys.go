package store

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/raskrebs/sonar/internal/state"
)

// Match keys identify a port across restarts, so a rename or a group pin
// survives a new PID and, for sonar-started runs, a new port. Daemon spec,
// "Renames and pins: match keys".
//
// Key forms, most specific first:
//
//	run:<group>/<name>                       sonar start run
//	docker:<compose_project>/<compose_service>
//	docker:<container>                       container without compose labels
//	cwd:<project_root>:<port>                native process
//	port:<port>                              last resort
const (
	KeyPrefixRun    = "run:"
	KeyPrefixDocker = "docker:"
	KeyPrefixCwd    = "cwd:"
	KeyPrefixPort   = "port:"
)

// MatchKeys returns every key form p can be matched against, most specific
// first. A scan looks a port up under each of them in turn and takes the first
// hit, so a rename recorded under any form keeps working.
//
// port:<port> is always the final entry. The spec only mentions it "when
// there is no cwd", which is true of the key a rename is *stored* under
// (PrimaryKey) — but a port that had no cwd when it was renamed may well have
// one later, and the rename has to keep applying.
func MatchKeys(p state.Port) []string {
	keys := make([]string, 0, 4)
	if p.Run != nil && p.Run.Name != "" {
		keys = append(keys, fmt.Sprintf("%s%s/%s", KeyPrefixRun, p.Run.Group, p.Run.Name))
	}
	if p.Docker != nil {
		switch {
		case p.Docker.ComposeProject != "" && p.Docker.ComposeService != "":
			keys = append(keys, fmt.Sprintf("%s%s/%s", KeyPrefixDocker,
				p.Docker.ComposeProject, p.Docker.ComposeService))
		case p.Docker.Container != "":
			keys = append(keys, KeyPrefixDocker+p.Docker.Container)
		}
	}
	if root := portRoot(p); root != "" {
		keys = append(keys, fmt.Sprintf("%s%s:%d", KeyPrefixCwd, root, p.Port))
	}
	keys = append(keys, fmt.Sprintf("%s%d", KeyPrefixPort, p.Port))
	return keys
}

// PrimaryKey is the key a new rename or pin for p is stored under: the most
// specific form available.
func PrimaryKey(p state.Port) string { return MatchKeys(p)[0] }

// portRoot is the directory a cwd: key is built from — the resolved project
// root, falling back to the process's working directory when the port could
// not be attributed to a project.
func portRoot(p state.Port) string {
	root := ""
	if p.ProjectRoot != nil {
		root = strings.TrimSpace(*p.ProjectRoot)
	}
	if root == "" {
		root = strings.TrimSpace(p.Cwd)
	}
	if root == "" {
		return ""
	}
	return filepath.Clean(root)
}

// Lookup finds p in a key→value map loaded with Renames or Pins. The scanner
// loads the maps once per tick instead of querying per port.
func Lookup(m map[string]string, p state.Port) (string, bool) {
	if len(m) == 0 {
		return "", false
	}
	for _, k := range MatchKeys(p) {
		if v, ok := m[k]; ok {
			return v, true
		}
	}
	return "", false
}

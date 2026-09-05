package spawn

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/raskrebs/sonar/internal/groups"
)

// Resolution is the group and name a run is registered under, plus the
// `.sonar.yaml` (if any) that decided them.
type Resolution struct {
	Group      string
	Name       string
	ConfigPath string
}

// Resolve implements steps 1 and 2 of the daemon spec's `sonar start`:
//
//	group: --group | nearest .sonar.yaml | git root | base name of cwd
//	name:  --name  | the .sonar.yaml service whose cmd is this command
//	       | InferName(argv)
//
// cwd may be empty, in which case the process working directory is used.
func Resolve(cwd string, argv []string, groupFlag, nameFlag string) Resolution {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	// One normalisation for the whole chain: the index, the git-root walk and
	// the directory-name fallback all have to see the same spelling of this
	// directory, or a cwd reached through a symlink (macOS $TMPDIR, /var vs
	// /private/var) resolves to a different group than the same directory
	// reached directly.
	cwd = groups.Canonical(cwd)

	index := groups.NewIndex()
	index.Observe(cwd)
	cfg := index.Nearest(cwd)

	res := Resolution{Group: strings.TrimSpace(groupFlag), Name: strings.TrimSpace(nameFlag)}
	if cfg != nil {
		res.ConfigPath = cfg.Path
	}

	if res.Group == "" {
		res.Group = inferGroup(cwd, cfg)
	}
	if res.Name == "" {
		if svc, ok := matchService(cfg, cwd, argv); ok {
			res.Name = svc
		} else {
			res.Name = InferName(argv)
		}
	}
	return res
}

// inferGroup is the group half of the chain, config first, then the checkout,
// then the directory the command was started in.
func inferGroup(cwd string, cfg *groups.Config) string {
	if cfg != nil && cfg.Name != "" {
		return cfg.Name
	}
	if root, worktree, ok := groups.Find(cwd); ok {
		return groups.GroupName(root, worktree)
	}
	return filepath.Base(cwd)
}

// matchService finds the service in cfg whose `cmd` is the command being
// started. The comparison is on the argv split, so quoting and repeated spaces
// in the YAML do not matter; a service with a `cwd` only matches when the
// command is being started there.
func matchService(cfg *groups.Config, cwd string, argv []string) (string, bool) {
	if cfg == nil || len(argv) == 0 {
		return "", false
	}
	want := strings.Join(argv, "\x00")
	for _, svc := range cfg.Services {
		if svc.Cmd == "" {
			continue
		}
		if strings.Join(splitCmd(svc.Cmd), "\x00") != want {
			continue
		}
		if svc.Cwd != "" && groups.Canonical(cfg.ServiceDir(svc)) != cwd {
			continue
		}
		return svc.Name, true
	}
	return "", false
}

// SplitCmd turns a `.sonar.yaml` `cmd:` string into argv with shell-style
// quoting and no shell execution (contract §4). Backslash escapes are honoured
// outside single quotes only, so a Windows path in double quotes survives.
func SplitCmd(s string) []string { return splitCmd(s) }

func splitCmd(s string) []string {
	var (
		out   []string
		cur   strings.Builder
		have  bool
		quote rune
	)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote == 0 && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		case quote == 0 && (c == '\'' || c == '"'):
			quote, have = c, true
		case quote != 0 && c == quote:
			quote = 0
		case quote != '\'' && c == '\\' && i+1 < len(runes):
			i++
			cur.WriteRune(runes[i])
			have = true
		default:
			cur.WriteRune(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

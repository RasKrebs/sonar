package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/install"
)

// The agent integrations are checked by reading exactly the files
// `sonar install` writes — never by running a client's CLI, which would be slow
// and, for a tool mid-login, interactive. A tool that is not installed at all
// is a `skip`: not having Cursor is not a problem with sonar.

// agentConfig is one file that may carry sonar's entry, and how to tell.
type agentConfig struct {
	scope string
	path  string
	has   func([]byte) bool
}

// tool is one coding agent sonar can register with.
type tool struct {
	id      string
	label   string
	client  string // the `sonar install mcp` flag, without the dashes
	scope   string // the scope that flag defaults to
	present func(*Env) bool
	configs func(*Env) []agentConfig
}

// mcpTools is the table behind the mcp_registered.* checks.
func mcpTools() []check {
	var out []check
	for _, t := range agentTools() {
		t := t
		out = append(out, check{
			id:  "mcp_registered." + t.id,
			run: func(_ context.Context, env *Env) rpc.DoctorCheck { return checkMCPRegistered(env, t) },
		})
	}
	return out
}

func agentTools() []tool {
	return []tool{
		{
			id: "claude_code", label: "Claude Code", client: "claude-code", scope: "project",
			present: func(env *Env) bool {
				return onPATH(env, "claude") ||
					exists(env.homePath(".claude")) ||
					exists(env.homePath(".claude.json"))
			},
			configs: func(env *Env) []agentConfig {
				var cc []agentConfig
				if root := gitRootOf(env.Project); root != "" {
					cc = append(cc, agentConfig{scope: "project", path: filepath.Join(root, ".mcp.json"), has: hasJSONServer})
				}
				// `sonar install mcp --claude-code --scope user` drives
				// `claude mcp add`, which writes this file. Sonar never edits
				// it, but reading it is how doctor sees a user-scope install.
				if p := env.homePath(".claude.json"); p != "" {
					cc = append(cc, agentConfig{scope: "user", path: p, has: hasJSONServer})
				}
				return cc
			},
		},
		{
			id: "cursor", label: "Cursor", client: "cursor", scope: "project",
			present: func(env *Env) bool {
				return onPATH(env, "cursor") || exists(env.homePath(".cursor"))
			},
			configs: func(env *Env) []agentConfig {
				var cc []agentConfig
				if root := gitRootOf(env.Project); root != "" {
					cc = append(cc, agentConfig{scope: "project", path: filepath.Join(root, ".cursor", "mcp.json"), has: hasJSONServer})
				}
				if p := env.homePath(".cursor", "mcp.json"); p != "" {
					cc = append(cc, agentConfig{scope: "user", path: p, has: hasJSONServer})
				}
				return cc
			},
		},
		{
			id: "codex", label: "Codex", client: "codex", scope: "user",
			present: func(env *Env) bool {
				return onPATH(env, "codex") || exists(env.homePath(".codex"))
			},
			configs: func(env *Env) []agentConfig {
				p := env.homePath(".codex", "config.toml")
				if p == "" {
					return nil
				}
				return []agentConfig{{scope: "user", path: p, has: hasTOMLServer}}
			},
		},
	}
}

func checkMCPRegistered(env *Env, t tool) rpc.DoctorCheck {
	if !t.present(env) {
		return rpc.DoctorCheck{
			Status:  StatusSkip,
			Summary: t.label + " is not installed",
			Detail:  "nothing to register sonar with on this machine",
		}
	}
	var looked []string
	for _, cfg := range t.configs(env) {
		looked = append(looked, cfg.path)
		data, err := os.ReadFile(cfg.path)
		if err != nil {
			continue
		}
		if cfg.has(data) {
			return rpc.DoctorCheck{
				Status:  StatusOK,
				Summary: fmt.Sprintf("registered with %s (%s scope)", t.label, cfg.scope),
				Detail:  cfg.path,
			}
		}
	}
	fix := fmt.Sprintf("sonar install mcp --%s", t.client)
	return rpc.DoctorCheck{
		Status:  StatusWarn,
		Summary: "not registered with " + t.label,
		Detail:  "no sonar entry in " + strings.Join(looked, " or "),
		Fix:     fix,
		Fixable: true,
	}
}

// hasJSONServer reports whether an MCP client config carries sonar's server
// entry. Only the key matters: a user who edited the command or added args
// still has a working registration.
func hasJSONServer(data []byte) bool {
	var root struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return false
	}
	_, ok := root.Servers["sonar"]
	return ok
}

// hasTOMLServer looks for codex's `[mcp_servers.sonar]` table header. Sonar has
// no TOML parser and needs none: the header is the whole question, and
// `codex mcp add` writes it on a line of its own.
var codexTable = regexp.MustCompile(`(?m)^\s*\[mcp_servers\.(sonar|"sonar")\]\s*$`)

func hasTOMLServer(data []byte) bool { return codexTable.Match(data) }

func onPATH(env *Env, name string) bool {
	_, err := env.LookPath(name)
	return err == nil
}

// claudeCode is the tool the skill and the hooks belong to; both `sonar install
// skills` and `sonar install hooks` support Claude Code only.
func claudeCode() tool { return agentTools()[0] }

func checkSkillsInstalled(_ context.Context, env *Env) rpc.DoctorCheck {
	cc := claudeCode()
	if !cc.present(env) {
		return rpc.DoctorCheck{
			Status:  StatusSkip,
			Summary: cc.label + " is not installed",
			Detail:  "the bundled skill has nowhere to go",
		}
	}
	root := gitRootOf(env.Project)
	want := install.SkillContent()

	var looked []string
	for _, scope := range []struct {
		name string
		s    install.Scope
	}{{"user", install.ScopeUser}, {"project", install.ScopeProject}} {
		if scope.s == install.ScopeProject && root == "" {
			continue
		}
		path := install.SkillPath(scope.s, env.Home, root)
		looked = append(looked, path)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		switch {
		case string(data) == want:
			return rpc.DoctorCheck{
				Status:  StatusOK,
				Summary: fmt.Sprintf("skill installed (%s scope)", scope.name),
				Detail:  path,
			}
		case install.IsManaged(string(data)):
			return rpc.DoctorCheck{
				Status:  StatusWarn,
				Summary: "the installed skill is out of date",
				Detail:  path + " was written by an older sonar",
				Fix:     "sonar install skills --claude-code",
				Fixable: true,
			}
		default:
			return rpc.DoctorCheck{
				Status:  StatusWarn,
				Summary: "a skill sonar did not write is in the way",
				Detail:  path + " exists and carries no sonar marker",
				Fix:     "sonar install skills --claude-code --force (this overwrites it)",
			}
		}
	}
	return rpc.DoctorCheck{
		Status:  StatusWarn,
		Summary: "the sonar skill is not installed",
		Detail:  "no skill at " + strings.Join(looked, " or "),
		Fix:     "sonar install skills --claude-code",
		Fixable: true,
	}
}

func checkHooksInstalled(_ context.Context, env *Env) rpc.DoctorCheck {
	cc := claudeCode()
	if !cc.present(env) {
		return rpc.DoctorCheck{
			Status:  StatusSkip,
			Summary: cc.label + " is not installed",
			Detail:  "there are no settings to add hooks to",
		}
	}
	root := gitRootOf(env.Project)

	var looked []string
	for _, scope := range []struct {
		name string
		s    install.Scope
	}{{"project", install.ScopeProject}, {"user", install.ScopeUser}} {
		if scope.s == install.ScopeProject && root == "" {
			continue
		}
		path := install.SettingsPath(scope.s, env.Home, root)
		looked = append(looked, path)
		n, err := install.InstalledHooks(path)
		if err != nil {
			return rpc.DoctorCheck{
				Status:  StatusWarn,
				Summary: "cannot read the Claude Code settings",
				Detail:  err.Error(),
				Fix:     "fix the JSON in " + path,
			}
		}
		if n > 0 {
			return rpc.DoctorCheck{
				Status:  StatusOK,
				Summary: fmt.Sprintf("%d sonar %s installed (%s scope)", n, plural(n, "hook"), scope.name),
				Detail:  path,
			}
		}
	}
	return rpc.DoctorCheck{
		Status:  StatusWarn,
		Summary: "the sonar hooks are not installed",
		Detail: "no _sonar entries in " + strings.Join(looked, " or ") +
			"; the hooks are optional — they attribute a session's processes and suggest `sonar start --`",
		Fix:     "sonar install hooks --claude-code",
		Fixable: true,
	}
}

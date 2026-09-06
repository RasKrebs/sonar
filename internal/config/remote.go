package config

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file is the `remote.hosts` half of the user config: the registered SSH
// hosts the daemon keeps a bridge to. It is written by `sonar remote add`,
// editable by hand, and read by the daemon's connection manager on start and
// on every `remote.add`/`remote.remove`.
//
// Reads go through Load (the typed Config); writes go through the map store,
// so a key this build does not know about survives the round trip.

// RemoteConfig is the `remote:` block of the user config.
type RemoteConfig struct {
	Hosts []RemoteHost `yaml:"hosts"`
}

// RemoteHost is one registered SSH host. Nothing here is a secret: identities
// and agents are the user's own `ssh` configuration, and sonar never stores a
// password.
type RemoteHost struct {
	// Name is how the host is addressed everywhere else: `--host <name>`, the
	// `host` field on every row it contributes, and the "<name>/" prefix on
	// its delta keys. Lowercase letters, digits and dashes.
	Name string `yaml:"name" json:"name"`
	// Target is what `ssh` receives, verbatim: "deploy@203.0.113.7", or a
	// `~/.ssh/config` alias. sonar never resolves it as DNS itself.
	Target string `yaml:"target" json:"target"`
	// SSHArgs are extra arguments placed before the target ("-J bastion").
	SSHArgs []string `yaml:"ssh_args,omitempty" json:"ssh_args,omitempty"`
	// Identity is an `ssh -i` key file. Empty leaves key selection to ssh.
	Identity string `yaml:"identity,omitempty" json:"identity,omitempty"`
	// Port is an `ssh -p` port. Zero leaves it to ssh.
	Port int `yaml:"port,omitempty" json:"port,omitempty"`
	// RemoteBin is the sonar binary to run on the far side. Empty means
	// "sonar", found on the login shell's PATH.
	RemoteBin string `yaml:"remote_bin,omitempty" json:"remote_bin,omitempty"`
}

// hostNamePattern is the spec's rule: host names are [a-z0-9-]+, unique, and
// never resolved as DNS by sonar.
var hostNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidHostName reports whether name is a usable remote host name.
func ValidHostName(name string) bool { return hostNamePattern.MatchString(name) }

// DefaultHostName derives a host name from an SSH target: the host part,
// lowercased, with everything outside [a-z0-9-] turned into a dash. It is what
// `sonar remote add deploy@203.0.113.7` uses when no --name is given.
func DefaultHostName(target string) string {
	t := target
	if _, after, ok := strings.Cut(t, "@"); ok {
		t = after
	}
	t = strings.ToLower(strings.TrimSpace(t))
	var b strings.Builder
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	return name
}

// RemoteHosts reads the registered hosts, dropping any entry that is not
// usable and returning a warning for each. A missing config is no hosts and no
// warnings.
func RemoteHosts() ([]RemoteHost, []string) {
	cfg, _ := Load()
	return validateHosts(cfg.Remote.Hosts)
}

func validateHosts(in []RemoteHost) ([]RemoteHost, []string) {
	var warnings []string
	seen := map[string]bool{}
	out := make([]RemoteHost, 0, len(in))
	for _, h := range in {
		h.Name = strings.TrimSpace(h.Name)
		h.Target = strings.TrimSpace(h.Target)
		switch {
		case !ValidHostName(h.Name):
			warnings = append(warnings,
				fmt.Sprintf("config: invalid remote host name %q — ignoring (names are [a-z0-9-]+)", h.Name))
		case h.Target == "":
			warnings = append(warnings,
				fmt.Sprintf("config: remote host %q has no target — ignoring", h.Name))
		case seen[h.Name]:
			warnings = append(warnings,
				fmt.Sprintf("config: duplicate remote host %q — ignoring the second one", h.Name))
		default:
			seen[h.Name] = true
			out = append(out, h)
		}
	}
	return out, warnings
}

// SaveRemoteHosts writes the host list to `remote.hosts`, leaving every other
// setting — and every key this build does not know about — untouched.
func SaveRemoteHosts(hosts []RemoteHost) error {
	if hosts == nil {
		hosts = []RemoteHost{}
	}
	value, err := yamlValue(hosts)
	if err != nil {
		return err
	}
	_, err = Apply(map[string]any{"remote": map[string]any{"hosts": value}})
	return err
}

// yamlValue round-trips a typed value into the plain map/slice form the config
// store writes, so the file keeps its normal YAML shape rather than gaining a
// Go-specific encoding.
func yamlValue(v any) (any, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding remote hosts: %w", err)
	}
	var out any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("encoding remote hosts: %w", err)
	}
	if out == nil {
		return []any{}, nil
	}
	return normalize(out), nil
}

// UnmarshalYAML accepts both forms of a `remote.hosts` entry: the mapping this
// build writes, and the bare `user@host` string cross-spec contract §4
// originally specified. A scalar becomes a host whose name is derived from the
// target, so a config written before `sonar remote add` existed keeps working
// and is upgraded in place the next time the list is saved.
func (h *RemoteHost) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var target string
		if err := node.Decode(&target); err != nil {
			return err
		}
		*h = RemoteHost{Name: DefaultHostName(target), Target: target}
		return nil
	}
	type plain RemoteHost
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*h = RemoteHost(p)
	return nil
}

package desktop

import (
	"github.com/raskrebs/sonar/internal/config"
)

// State is what the CLI remembers about an install between runs: enough for
// `--check` and `--update` to answer without opening the bundle, and enough
// for `sonar tray` and the doctor's desktop_installed check to find an app
// that was installed somewhere non-standard with --dir.
type State struct {
	Version string `json:"version"`
	Path    string `json:"path"`
}

// StateStore is where that lives. It is an interface for one reason: a test
// should be able to assert what was recorded without reading YAML back.
type StateStore interface {
	Load() (State, error)
	Save(State) error
}

// ConfigStore keeps the state in the user's config.yaml, under
// desktop.installed_version and desktop.installed_path.
type ConfigStore struct{}

func (ConfigStore) Load() (State, error) {
	cfg, _ := config.Load()
	return State{Version: cfg.Desktop.InstalledVersion, Path: cfg.Desktop.InstalledPath}, nil
}

func (ConfigStore) Save(s State) error {
	_, err := config.Apply(map[string]any{"desktop": map[string]any{
		"installed_version": s.Version,
		"installed_path":    s.Path,
	}})
	return err
}

// MemoryStore is a StateStore that forgets, for tests and for a run that must
// not touch the config at all.
type MemoryStore struct{ State State }

func (m *MemoryStore) Load() (State, error) { return m.State, nil }
func (m *MemoryStore) Save(s State) error   { m.State = s; return nil }

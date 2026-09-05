package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file is the read/modify/write half of the config used by the daemon's
// `config.get`, `config.set` and `config.path` methods. It works on a plain
// map rather than the Config struct so a key this build does not know about
// survives a round trip — a newer app writing `remote.hosts` must not have it
// deleted by an older daemon.
//
// Comments do not survive: the file is re-marshalled from the map. The
// pre-write copy at <path>.bak is what makes that recoverable.

// BackupSuffix is appended to the config path for the copy Save keeps of the
// file it is about to replace.
const BackupSuffix = ".bak"

// Map reads the config file into a JSON-safe map. A missing file is an empty
// map, not an error: there is nothing wrong with never having written one.
func Map() (map[string]any, error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", Path(), err)
	}
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", Path(), err)
	}
	m, ok := normalize(raw).(map[string]any)
	if !ok || m == nil {
		return map[string]any{}, nil
	}
	return m, nil
}

// Apply merges patch into the config file and writes it back. A null value in
// the patch removes the key; a nested map is merged key by key rather than
// replacing the whole subtree, so setting `list.sort` leaves `list.columns`
// alone. The written config is returned.
//
// The merged config has to parse and validate as a Config; if it does not,
// nothing is written and the validation messages come back as the error.
func Apply(patch map[string]any) (map[string]any, error) {
	current, err := Map()
	if err != nil {
		return nil, err
	}
	merged := merge(current, patch)
	if err := validateMap(merged); err != nil {
		return nil, err
	}
	if err := Save(merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// Save writes m to the config file, copying whatever is there to <path>.bak
// first. The file is written through a temporary file and renamed, so a crash
// mid-write cannot leave a half-written config behind.
func Save(m map[string]any) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		if err := backup(path, mode); err != nil {
			return err
		}
	}

	body, err := yaml.Marshal(denormalize(m))
	if err != nil {
		return fmt.Errorf("encoding the config: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

// BackupPath is where Save parks the previous config.
func BackupPath() string { return Path() + BackupSuffix }

func backup(path string, mode os.FileMode) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if err := os.WriteFile(path+BackupSuffix, data, mode); err != nil {
		return fmt.Errorf("writing %s: %w", path+BackupSuffix, err)
	}
	return nil
}

// merge deep-merges patch into base and returns a new map. A nil value deletes
// the key; two maps merge; anything else replaces.
func merge(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(patch))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		if v == nil {
			delete(out, k)
			continue
		}
		sub, isMap := normalize(v).(map[string]any)
		cur, wasMap := out[k].(map[string]any)
		if isMap && wasMap {
			out[k] = merge(cur, sub)
			continue
		}
		out[k] = normalize(v)
	}
	return out
}

// validateMap checks that a merged config still decodes into Config and that
// every setting is one this build accepts.
func validateMap(m map[string]any) error {
	body, err := yaml.Marshal(denormalize(m))
	if err != nil {
		return fmt.Errorf("encoding the config: %w", err)
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(body, cfg); err != nil {
		return fmt.Errorf("the merged config is not valid: %w", err)
	}
	if warnings := validate(cfg); len(warnings) > 0 {
		return fmt.Errorf("%s", strings.Join(warnings, "; "))
	}
	return nil
}

// normalize turns what YAML decoding produces into JSON-safe values: map keys
// become strings and nested containers are converted in place.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalize(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalize(t[i])
		}
		return out
	default:
		return v
	}
}

// denormalize is the inverse of normalize for the one case that matters:
// `services` is keyed by port number, and writing "9000" as a quoted string
// would stop Load from parsing it back.
func denormalize(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		if list, isList := v.([]any); isList {
			out := make([]any, len(list))
			for i := range list {
				out[i] = denormalize(list[i])
			}
			return out
		}
		return v
	}
	numeric := len(m) > 0
	for k := range m {
		if _, err := strconv.Atoi(k); err != nil {
			numeric = false
			break
		}
	}
	if !numeric {
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[k] = denormalize(val)
		}
		return out
	}
	out := make(map[int]any, len(m))
	for k, val := range m {
		n, _ := strconv.Atoi(k)
		out[n] = denormalize(val)
	}
	return out
}

package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// serverKey is the name sonar registers itself under in every client config.
const serverKey = "sonar"

// serversKey is the JSON object holding MCP servers in the Claude Code and
// Cursor config formats.
const serversKey = "mcpServers"

// Options describes one install run. GitRoot, Home, Binary, LookPath and Run
// are injected so the whole package is testable without a real environment.
type Options struct {
	Client    Client
	Scope     Scope
	Print     bool
	Uninstall bool
	Force     bool

	GitRoot string
	Home    string
	Binary  string

	// LookPath finds a client CLI; defaults to exec.LookPath.
	LookPath func(string) (string, error)
	// Run executes a client CLI; defaults to running it with inherited stdio.
	Run func(argv []string) error
}

// Result is what happened, for the CLI to render. Output is non-empty when the
// caller should print something (a --print payload or a manual fallback).
type Result struct {
	Action   Action
	Path     string
	Command  []string
	Output   string
	Warnings []string
}

// ErrClientCLIMissing is returned when a command-driven client's CLI is not on
// PATH. The Result still carries the manual instructions to print.
var ErrClientCLIMissing = errors.New("client CLI not found on PATH")

func (o Options) lookPath() func(string) (string, error) {
	if o.LookPath != nil {
		return o.LookPath
	}
	return exec.LookPath
}

func (o Options) run() func([]string) error {
	if o.Run != nil {
		return o.Run
	}
	return func(argv []string) error {
		c := exec.Command(argv[0], argv[1:]...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return c.Run()
	}
}

// InstallMCP installs, prints or removes sonar's MCP server entry for one
// client. It never writes anything when opts.Print is set.
func InstallMCP(opts Options) (Result, error) {
	target, err := ResolveTarget(opts.Client, opts.Scope, opts.GitRoot, opts.Home)
	if err != nil {
		return Result{}, err
	}
	switch target.Kind {
	case TargetSnippet:
		return snippetResult(opts)
	case TargetCommand:
		return commandResult(opts, target)
	default:
		return fileResult(opts, target.Path)
	}
}

// serverEntry builds the canonical {"command": ..., "args": ["mcp"]} object.
func serverEntry(binary string) *Object {
	entry := NewObject()
	entry.Set("command", binary)
	entry.Set("args", []any{"mcp"})
	return entry
}

// snippetJSON renders the snippet a generic MCP client needs pasted in.
func snippetJSON(binary string) string {
	root := NewObject()
	servers := NewObject()
	servers.Set(serverKey, serverEntry(binary))
	root.Set(serversKey, servers)
	out, err := Encode(root, "  ")
	if err != nil {
		// Encode only fails on values we did not construct here.
		return ""
	}
	return string(out)
}

func snippetResult(opts Options) (Result, error) {
	if opts.Uninstall {
		return Result{
			Action: ActionPrinted,
			Output: fmt.Sprintf("Remove the %q entry from your MCP client's server list.\n", serverKey),
		}, nil
	}
	return Result{Action: ActionPrinted, Output: snippetJSON(opts.Binary)}, nil
}

// codexTOML is the manual fallback when the codex CLI is unavailable.
func codexTOML(binary string) string {
	command, _ := json.Marshal(binary)
	return fmt.Sprintf("[mcp_servers.%s]\ncommand = %s\nargs = [\"mcp\"]\n", serverKey, command)
}

func commandResult(opts Options, target Target) (Result, error) {
	source := target.Install
	if opts.Uninstall {
		source = target.Uninstall
	}
	argv := make([]string, 0, len(source))
	for _, a := range source {
		if a == binaryPlaceholder {
			a = opts.Binary
		}
		argv = append(argv, a)
	}

	res := Result{Command: argv}
	if opts.Print {
		res.Action = ActionPrinted
		res.Output = strings.Join(argv, " ") + "\n"
		return res, nil
	}

	if _, err := opts.lookPath()(target.Tool); err != nil {
		res.Action = ActionPrinted
		if opts.Client == ClientCodex {
			res.Output = fmt.Sprintf("codex is not on PATH. Add this to ~/.codex/config.toml:\n\n%s", codexTOML(opts.Binary))
		} else {
			res.Output = fmt.Sprintf("%s is not on PATH. Run this once it is installed:\n\n  %s\n", target.Tool, strings.Join(argv, " "))
		}
		return res, fmt.Errorf("%w: %s", ErrClientCLIMissing, target.Tool)
	}

	if err := opts.run()(argv); err != nil {
		return res, fmt.Errorf("%s failed: %w", strings.Join(argv, " "), err)
	}
	res.Action = ActionRan
	return res, nil
}

func fileResult(opts Options, path string) (Result, error) {
	res := Result{Path: path}

	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		existing = nil
	default:
		return res, fmt.Errorf("could not read %s: %w", path, err)
	}

	indent := "  "
	root := NewObject()
	fileExists := existing != nil
	if fileExists {
		if HasComments(existing) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s contains comments; rewriting it will drop them", path))
		}
		indent = DetectIndent(existing)
		if strings.TrimSpace(string(existing)) == "" {
			fileExists = false
		} else {
			root, err = ParseObject(existing)
			if err != nil {
				return res, fmt.Errorf("refusing to touch %s: %w", path, err)
			}
		}
	}

	servers, ok := root.Object(serversKey)
	if !ok {
		if _, present := root.Get(serversKey); present {
			return res, fmt.Errorf("refusing to touch %s: %q is not an object", path, serversKey)
		}
		servers = NewObject()
		root.Set(serversKey, servers)
	}

	if opts.Uninstall {
		return uninstallFromFile(opts, res, root, servers, indent, fileExists)
	}
	return installIntoFile(opts, res, root, servers, indent, fileExists)
}

func installIntoFile(opts Options, res Result, root, servers *Object, indent string, fileExists bool) (Result, error) {
	want := serverEntry(opts.Binary)
	wantJSON, err := Encode(want, "")
	if err != nil {
		return res, err
	}

	action := ActionCreated
	if fileExists {
		action = ActionUpdated
	}
	if current, ok := servers.Get(serverKey); ok {
		if currentObj, isObj := current.(*Object); isObj {
			gotJSON, err := Encode(currentObj, "")
			if err != nil {
				return res, err
			}
			if string(gotJSON) == string(wantJSON) {
				res.Action = ActionUnchanged
				return res, nil
			}
		}
		if !opts.Force {
			return res, fmt.Errorf("%s already has an %s.%s entry that sonar did not write; re-run with --force to replace it", res.Path, serversKey, serverKey)
		}
		action = ActionUpdated
	}
	servers.Set(serverKey, want)

	out, err := Encode(root, indent)
	if err != nil {
		return res, err
	}
	if opts.Print {
		res.Action = ActionPrinted
		res.Output = string(out)
		return res, nil
	}
	if err := writeFile(res.Path, out); err != nil {
		return res, err
	}
	res.Action = action
	return res, nil
}

func uninstallFromFile(opts Options, res Result, root, servers *Object, indent string, fileExists bool) (Result, error) {
	if !fileExists {
		res.Action = ActionAbsent
		return res, nil
	}
	current, present := servers.Get(serverKey)
	if !present {
		res.Action = ActionAbsent
		return res, nil
	}
	if currentObj, ok := current.(*Object); ok {
		gotJSON, err := Encode(currentObj, "")
		if err != nil {
			return res, err
		}
		wantJSON, err := Encode(serverEntry(opts.Binary), "")
		if err != nil {
			return res, err
		}
		if string(gotJSON) != string(wantJSON) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("removed an %s.%s entry sonar did not write: %s", serversKey, serverKey, strings.TrimSpace(string(gotJSON))))
		}
	}
	servers.Delete(serverKey)

	out, err := Encode(root, indent)
	if err != nil {
		return res, err
	}
	if opts.Print {
		res.Action = ActionPrinted
		res.Output = string(out)
		return res, nil
	}
	if err := writeFile(res.Path, out); err != nil {
		return res, err
	}
	res.Action = ActionRemoved
	return res, nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("could not create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("could not write %s: %w", path, err)
	}
	return nil
}

package spawn

import "strings"

// managerVerbs maps a launcher binary to the sub-commands that carry no
// information about what is being started. `npm run dev` is the dev server, not
// "npm" and not "run", so both are stepped over and the name is `dev`.
//
// A launcher listed here also accepts nested launchers (`docker compose up db`)
// because the sub-command's own verb set is merged in as the walk proceeds.
var managerVerbs = map[string]map[string]bool{
	"npm":            {"run": true, "run-script": true, "exec": true},
	"pnpm":           {"run": true, "exec": true, "dlx": true},
	"yarn":           {"run": true, "exec": true, "dlx": true},
	"bun":            {"run": true, "x": true},
	"npx":            {},
	"bunx":           {},
	"deno":           {"run": true, "task": true},
	"uv":             {"run": true, "tool": true},
	"uvx":            {},
	"poetry":         {"run": true},
	"pipenv":         {"run": true},
	"pdm":            {"run": true},
	"rye":            {"run": true},
	"hatch":          {"run": true},
	"python":         {},
	"python3":        {},
	"py":             {},
	"node":           {},
	"go":             {"run": true},
	"cargo":          {"run": true},
	"docker":         {"compose": true, "up": true, "run": true, "start": true, "exec": true},
	"docker-compose": {"up": true, "run": true, "start": true},
	"make":           {},
	"just":           {},
	"task":           {},
	"bundle":         {"exec": true},
	"dotnet":         {"run": true},
	"mvn":            {},
	"gradle":         {},
	"pixi":           {"run": true},
	"mise":           {"run": true, "exec": true},
	"nix":            {"run": true, "develop": true},
	"env":            {},
}

// valueFlags are flags that swallow the next token, so the token after them is
// never the service name (`npm --prefix ./web run dev`).
var valueFlags = map[string]bool{
	"--prefix": true, "--cwd": true, "--dir": true, "-C": true, "-c": true,
	"--workspace": true, "-w": true, "--filter": true, "-f": true,
	"--file": true, "--project-name": true, "-p": true, "--project": true,
	"--chdir": true, "--env-file": true,
}

// InferName picks a service name from a command line, the fallback for
// `sonar start` without `--name` (daemon spec, "sonar start" step 2).
//
//	npm run dev          -> dev
//	uv run api           -> api
//	python -m uvicorn    -> uvicorn
//	./dev.sh             -> dev.sh
//	docker compose up db -> db
//
// It never returns a path: the last path element wins, extension included, so
// `./dev.sh` stays recognisable and `go run ./cmd/api` becomes `api`.
func InferName(argv []string) string {
	toks := stripAssignments(argv)
	if len(toks) == 0 {
		return ""
	}

	head := commandToken(toks[0])
	verbs, isManager := managerVerbs[head]
	if !isManager {
		return nameFromToken(toks[0])
	}

	// Copy: nested launchers merge their verbs in and must not mutate the table.
	skip := make(map[string]bool, len(verbs)+4)
	for v := range verbs {
		skip[v] = true
	}

	for i := 1; i < len(toks); i++ {
		tok := toks[i]
		if tok == "--" {
			continue
		}
		if strings.HasPrefix(tok, "-") {
			if valueFlags[flagName(tok)] && !strings.Contains(tok, "=") {
				i++ // its value is not a name either
			}
			continue
		}
		word := commandToken(tok)
		if skip[word] {
			continue
		}
		if nested, ok := managerVerbs[word]; ok {
			for v := range nested {
				skip[v] = true
			}
			continue
		}
		return nameFromToken(tok)
	}
	return head
}

// stripAssignments drops leading `VAR=value` tokens so `PORT=3000 npm run dev`
// still infers `dev`.
func stripAssignments(argv []string) []string {
	for i, tok := range argv {
		if strings.HasPrefix(tok, "-") || !strings.Contains(tok, "=") {
			return argv[i:]
		}
		if key, _, _ := strings.Cut(tok, "="); key == "" || strings.ContainsAny(key, "/\\.") {
			return argv[i:]
		}
	}
	return nil
}

// commandToken reduces a token to the binary name it names: no directory, no
// Windows extension, lower-cased for table lookup.
func commandToken(tok string) string {
	base := nameFromToken(tok)
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".cmd")
	base = strings.TrimSuffix(base, ".bat")
	return strings.ToLower(base)
}

// nameFromToken is the last path element of a token, with both separators
// honoured so a Windows path works on a Unix daemon and vice versa.
func nameFromToken(tok string) string {
	tok = strings.TrimSuffix(tok, "/")
	tok = strings.TrimSuffix(tok, `\`)
	if i := strings.LastIndexAny(tok, `/\`); i >= 0 {
		tok = tok[i+1:]
	}
	if tok == "" {
		return ""
	}
	return tok
}

// flagName is the flag part of `--flag=value`.
func flagName(tok string) string {
	name, _, _ := strings.Cut(tok, "=")
	return name
}

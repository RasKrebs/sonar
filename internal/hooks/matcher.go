// Package hooks recognises long-lived dev-server commands inside a shell
// command line, so the Claude Code PreToolUse hook can suggest running them
// through `sonar start` instead. It parses; it never executes anything.
//
// False positives are worse than misses here: a wrong suggestion trains the
// agent to ignore the hook. Every rule is therefore anchored on the command's
// first token plus an explicit sub-command list, with a deny list for the
// obvious non-server sub-commands (`vite build`, `go test`, ...).
package hooks

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Match describes a recognised dev-server invocation.
type Match struct {
	Segment string // the individual command as written, e.g. "npm run dev"
	Rule    string // id of the table entry that fired, e.g. "npm-script"
	Suggest string // "sonar start -- npm run dev"
}

// cmdRule matches a command by its first token plus an explicit set of
// sub-command prefixes.
type cmdRule struct {
	ID   string
	Cmd  string   // first token, compared by base name
	Subs []string // space-joined argument prefixes that mean "server"
	Deny []string // argument prefixes that mean "definitely not a server"
	Bare bool     // the command with no positional argument is a server

	// Always marks a single-purpose server binary: whatever its arguments,
	// running it starts a server (uvicorn takes an app, http-server a dir).
	Always bool
}

// cmdRules is the single table the spec asks for. Entries come from spec 2
// §2.3 plus the sibling dev servers of the frameworks it names.
var cmdRules = []cmdRule{
	{ID: "vite", Cmd: "vite", Subs: []string{"dev", "serve", "preview"}, Deny: []string{"build", "optimize"}, Bare: true},
	{ID: "next", Cmd: "next", Subs: []string{"dev", "start"}, Deny: []string{"build", "lint", "export", "telemetry", "info"}},
	{ID: "nuxt", Cmd: "nuxt", Subs: []string{"dev"}, Deny: []string{"build", "generate", "prepare"}},
	{ID: "astro", Cmd: "astro", Subs: []string{"dev", "preview"}, Deny: []string{"build", "check", "add", "sync"}},
	{ID: "ng", Cmd: "ng", Subs: []string{"serve"}, Deny: []string{"build", "test", "lint", "generate"}},
	{ID: "webpack", Cmd: "webpack", Subs: []string{"serve"}, Deny: []string{"build"}},
	{ID: "webpack-dev-server", Cmd: "webpack-dev-server", Always: true},
	{ID: "http-server", Cmd: "http-server", Always: true},
	{ID: "uvicorn", Cmd: "uvicorn", Always: true},
	{ID: "gunicorn", Cmd: "gunicorn", Always: true},
	{ID: "flask", Cmd: "flask", Subs: []string{"run"}, Deny: []string{"db", "shell", "routes"}},
	{ID: "python", Cmd: "python", Subs: pythonServerSubs},
	{ID: "python3", Cmd: "python3", Subs: pythonServerSubs},
	{ID: "rails", Cmd: "rails", Subs: []string{"s", "server"}},
	{ID: "go", Cmd: "go", Subs: []string{"run"}},
	{ID: "cargo", Cmd: "cargo", Subs: []string{"run"}},
	{ID: "php", Cmd: "php", Subs: []string{"artisan serve", "-S"}},
	{ID: "dotnet", Cmd: "dotnet", Subs: []string{"run", "watch run", "watch"}},
}

// pythonServerSubs are the `python ...` forms that start a server. Bare
// `python foo.py` is deliberately absent: it is far more often a script.
var pythonServerSubs = []string{
	"-m http.server",
	"-m uvicorn",
	"-m flask",
	"-m gunicorn",
	"-m SimpleHTTPServer",
	"manage.py runserver",
}

// pkgRule matches `<pm> [run] <script>` where the script name says "server".
type pkgRule struct {
	ID          string
	Cmd         string
	RunWords    []string // sub-commands that introduce a script name
	ImplicitRun bool     // `<pm> dev` means `<pm> run dev`
	Builtins    []string // sub-commands that are themselves scripts (npm start)
}

var pkgRules = []pkgRule{
	{ID: "npm-script", Cmd: "npm", RunWords: []string{"run", "run-script"}, Builtins: []string{"start"}},
	{ID: "pnpm-script", Cmd: "pnpm", RunWords: []string{"run"}, ImplicitRun: true},
	{ID: "yarn-script", Cmd: "yarn", RunWords: []string{"run"}, ImplicitRun: true},
	{ID: "bun-script", Cmd: "bun", RunWords: []string{"run"}, ImplicitRun: true},
	{ID: "deno-script", Cmd: "deno", RunWords: []string{"task"}},
}

// devScripts are package.json / deno.json script names that start a server.
var devScripts = []string{"dev", "start", "serve", "develop", "preview"}

// devScriptPrefixes catch monorepo-style names such as "dev:web".
var devScriptPrefixes = []string{"dev:", "start:", "serve:", "dev-", "serve-"}

// runners are transparent prefixes: strip them and match what follows. This is
// how `uv run uvicorn app:app` fires while `uv run pytest` does not.
var runners = [][]string{
	{"uv", "run"},
	{"npm", "exec"},
	{"pnpm", "exec"},
	{"pnpm", "dlx"},
	{"yarn", "dlx"},
	{"poetry", "run"},
	{"pipenv", "run"},
	{"bundle", "exec"},
	{"rye", "run"},
	{"pdm", "run"},
	{"npx"},
	{"bunx"},
	{"nohup"},
	{"sudo"},
	{"command"},
	{"exec"},
	{"time"},
}

// infoFlags ask a command to print something and exit. `vite --version` is
// not a dev server, whatever the rest of the rule says.
var infoFlags = map[string]bool{
	"--version": true,
	"--help":    true,
	"-v":        true,
	"-V":        true,
	"-h":        true,
}

// isInformational reports whether the arguments only ask for version or help.
func isInformational(args []string) bool {
	for _, a := range args {
		if infoFlags[a] {
			return true
		}
	}
	return false
}

// maxRunnerDepth bounds how many transparent prefixes are peeled.
const maxRunnerDepth = 3

var assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// Detect returns the first dev-server invocation in a shell command line.
func Detect(command string) (Match, bool) {
	for _, seg := range splitSegments(command) {
		if id, ok := matchTokens(tokenize(seg), 0); ok {
			return Match{Segment: seg, Rule: id, Suggest: "sonar start -- " + seg}, true
		}
	}
	return Match{}, false
}

// matchTokens tests one command's tokens, peeling transparent runner prefixes
// so `npx vite` reaches the vite rule.
func matchTokens(tokens []string, depth int) (string, bool) {
	tokens = stripEnvPrefix(tokens)
	if len(tokens) == 0 {
		return "", false
	}
	name := base(tokens[0])
	if name == "sonar" {
		// Already managed by sonar; never suggest wrapping it again.
		return "", false
	}
	args := tokens[1:]
	if isInformational(args) {
		return "", false
	}

	for _, r := range pkgRules {
		if r.Cmd == name {
			if id, ok := matchPkgRule(r, args); ok {
				return id, true
			}
		}
	}
	for _, r := range cmdRules {
		if r.Cmd == name && matchCmdRule(r, args) {
			return r.ID, true
		}
	}

	if depth >= maxRunnerDepth {
		return "", false
	}
	if rest, ok := stripRunner(tokens); ok {
		return matchTokens(rest, depth+1)
	}
	return "", false
}

func matchPkgRule(r pkgRule, args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	head := args[0]
	for _, w := range r.RunWords {
		if head == w {
			if len(args) < 2 {
				return "", false
			}
			return r.ID, isDevScript(args[1])
		}
	}
	for _, b := range r.Builtins {
		if head == b {
			return r.ID, true
		}
	}
	if r.ImplicitRun {
		return r.ID, isDevScript(head)
	}
	return "", false
}

func matchCmdRule(r cmdRule, args []string) bool {
	joined := strings.Join(args, " ")
	for _, d := range r.Deny {
		if joined == d || strings.HasPrefix(joined, d+" ") {
			return false
		}
	}
	if r.Always {
		return true
	}
	for _, s := range r.Subs {
		if joined == s || strings.HasPrefix(joined, s+" ") {
			return true
		}
	}
	if r.Bare {
		// "bare" means no positional argument before the first flag.
		return len(args) == 0 || strings.HasPrefix(args[0], "-")
	}
	return false
}

func isDevScript(script string) bool {
	for _, s := range devScripts {
		if script == s {
			return true
		}
	}
	for _, p := range devScriptPrefixes {
		if strings.HasPrefix(script, p) {
			return true
		}
	}
	return false
}

// stripEnvPrefix removes a leading `env` and any VAR=value assignments.
func stripEnvPrefix(tokens []string) []string {
	for len(tokens) > 0 {
		if tokens[0] == "env" || assignRe.MatchString(tokens[0]) {
			tokens = tokens[1:]
			continue
		}
		break
	}
	return tokens
}

// stripRunner peels one transparent runner prefix plus any flags it carries.
func stripRunner(tokens []string) ([]string, bool) {
	for _, r := range runners {
		if len(tokens) <= len(r) || base(tokens[0]) != r[0] {
			continue
		}
		matched := true
		for i := 1; i < len(r); i++ {
			if tokens[i] != r[i] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		rest := tokens[len(r):]
		for len(rest) > 0 && rest[0] != "--" && strings.HasPrefix(rest[0], "-") {
			rest = rest[1:]
		}
		if len(rest) > 0 && rest[0] == "--" {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return nil, false
		}
		return rest, true
	}
	return nil, false
}

func base(tok string) string {
	if strings.ContainsAny(tok, "/\\") {
		return filepath.Base(filepath.ToSlash(tok))
	}
	return tok
}

// splitSegments splits a command line on shell separators, honouring quotes.
// A trailing `&` therefore just ends a segment, which is why a backgrounded
// dev server is recognised like any other.
func splitSegments(command string) []string {
	var segs []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if quote != 0 {
			cur.WriteRune(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			cur.WriteRune(c)
		case '\\':
			cur.WriteRune(c)
			if i+1 < len(runes) {
				i++
				cur.WriteRune(runes[i])
			}
		case ';', '\n', '&', '|':
			// consume a doubled && or ||
			if (c == '&' || c == '|') && i+1 < len(runes) && runes[i+1] == c {
				i++
			}
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	return segs
}

// tokenize splits one segment into words, unwrapping shell quoting. It does no
// expansion and never executes anything.
func tokenize(segment string) []string {
	var toks []string
	var cur strings.Builder
	var quote rune
	started := false
	flush := func() {
		if started {
			toks = append(toks, cur.String())
		}
		cur.Reset()
		started = false
	}
	runes := []rune(segment)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if quote != 0 {
			if c == quote {
				quote = 0
				continue
			}
			cur.WriteRune(c)
			started = true
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			started = true
		case '\\':
			if i+1 < len(runes) {
				i++
				cur.WriteRune(runes[i])
				started = true
			}
		case ' ', '\t':
			flush()
		default:
			cur.WriteRune(c)
			started = true
		}
	}
	flush()
	return toks
}

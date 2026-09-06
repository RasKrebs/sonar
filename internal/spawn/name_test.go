package spawn

import "testing"

func TestInferName(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"npm", "run", "dev"}, "dev"},
		{[]string{"pnpm", "run", "web"}, "web"},
		{[]string{"yarn", "dev"}, "dev"},
		{[]string{"bun", "run", "start"}, "start"},
		{[]string{"npx", "vite"}, "vite"},
		{[]string{"uv", "run", "api"}, "api"},
		{[]string{"uv", "run", "uvicorn", "app:app"}, "uvicorn"},
		{[]string{"python", "-m", "uvicorn"}, "uvicorn"},
		{[]string{"python3", "-m", "http.server", "8000"}, "http.server"},
		{[]string{"./dev.sh"}, "dev.sh"},
		{[]string{"/usr/local/bin/dev.sh"}, "dev.sh"},
		{[]string{"docker", "compose", "up", "db"}, "db"},
		{[]string{"docker-compose", "up", "worker"}, "worker"},
		{[]string{"go", "run", "./cmd/api"}, "api"},
		{[]string{"cargo", "run", "--bin", "server"}, "server"},
		{[]string{"npm", "--prefix", "./web", "run", "dev"}, "dev"},
		{[]string{"PORT=3000", "npm", "run", "dev"}, "dev"},
		{[]string{"poetry", "run", "flask"}, "flask"},
		{[]string{"node", "server.js"}, "server.js"},
		{[]string{"redis-server"}, "redis-server"},
		{[]string{"npm"}, "npm"},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := InferName(tc.argv); got != tc.want {
			t.Errorf("InferName(%q) = %q, want %q", tc.argv, got, tc.want)
		}
	}
}

func TestSplitCmd(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"npm run dev", []string{"npm", "run", "dev"}},
		{"  npm   run   dev  ", []string{"npm", "run", "dev"}},
		{`uv run uvicorn "app:app" --reload`, []string{"uv", "run", "uvicorn", "app:app", "--reload"}},
		{`sh -c 'echo hi there'`, []string{"sh", "-c", "echo hi there"}},
		{"", nil},
	}
	for _, tc := range cases {
		assertSplit(t, SplitCmd(tc.in), tc.want, tc.in)
	}
}

// TestSplitCmdUnixEscapes pins the POSIX reading: outside single quotes a
// backslash escapes whatever follows it.
func TestSplitCmdUnixEscapes(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`/tmp/a\ b/serve --port 3000`, []string{"/tmp/a b/serve", "--port", "3000"}},
		{`echo "say \"hi\""`, []string{"echo", `say "hi"`}},
		{`echo 'a\b'`, []string{"echo", `a\b`}},
		{`echo \\`, []string{"echo", `\`}},
	}
	for _, tc := range cases {
		assertSplit(t, splitCmdFor(tc.in, false), tc.want, tc.in)
	}
}

// TestSplitCmdWindowsKeepsBackslashes is the Windows regression: `\` is a path
// separator there, and treating it as an escape turned
// `C:\Users\me\listener.exe` into `C:Usersmelistener.exe`, which no `sonar up`
// could start. Only a quote inside double quotes is escapable, as
// CommandLineToArgvW reads it.
func TestSplitCmdWindowsKeepsBackslashes(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`C:\Users\me\listener.exe 3000`, []string{`C:\Users\me\listener.exe`, "3000"}},
		{`"C:\Program Files\app\app.exe" --port 3000`, []string{`C:\Program Files\app\app.exe`, "--port", "3000"}},
		{`'C:\Users\me\app.exe' serve`, []string{`C:\Users\me\app.exe`, "serve"}},
		{`app.exe --dir "C:\logs\\" --port 80`, []string{"app.exe", "--dir", `C:\logs\`, "--port", "80"}},
		{`echo "say \"hi\""`, []string{"echo", `say "hi"`}},
		{`npm run dev`, []string{"npm", "run", "dev"}},
	}
	for _, tc := range cases {
		assertSplit(t, splitCmdFor(tc.in, true), tc.want, tc.in)
	}
}

func assertSplit(t *testing.T, got, want []string, in string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("split(%q) = %q, want %q", in, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("split(%q) = %q, want %q", in, got, want)
		}
	}
}

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
		got := SplitCmd(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("SplitCmd(%q) = %q, want %q", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("SplitCmd(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	}
}

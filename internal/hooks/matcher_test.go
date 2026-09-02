package hooks

import "testing"

func TestDetectPositives(t *testing.T) {
	cases := []struct {
		cmd  string
		rule string
	}{
		// package-manager scripts
		{"npm run dev", "npm-script"},
		{"npm run dev -- --port 3000", "npm-script"},
		{"npm start", "npm-script"},
		{"npm run start", "npm-script"},
		{"npm run dev:web", "npm-script"},
		{"pnpm dev", "pnpm-script"},
		{"pnpm run dev", "pnpm-script"},
		{"yarn dev", "yarn-script"},
		{"yarn start", "yarn-script"},
		{"bun run dev", "bun-script"},
		{"bun dev", "bun-script"},
		{"deno task dev", "deno-script"},
		// bare dev servers
		{"vite", "vite"},
		{"vite dev", "vite"},
		{"vite --port 3000", "vite"},
		{"vite preview", "vite"},
		{"next dev", "next"},
		{"next start", "next"},
		{"nuxt dev", "nuxt"},
		{"astro dev", "astro"},
		{"ng serve", "ng"},
		{"webpack serve", "webpack"},
		{"webpack-dev-server", "webpack-dev-server"},
		{"http-server ./public", "http-server"},
		{"uvicorn app.main:app --reload", "uvicorn"},
		{"gunicorn wsgi:app", "gunicorn"},
		{"flask run", "flask"},
		{"python -m http.server 8000", "python"},
		{"python3 -m http.server", "python3"},
		{"python -m uvicorn app:app", "python"},
		{"rails s", "rails"},
		{"rails server -p 3000", "rails"},
		{"go run ./cmd/server", "go"},
		{"go run .", "go"},
		{"cargo run", "cargo"},
		{"php artisan serve", "php"},
		{"php -S localhost:8000", "php"},
		{"dotnet run", "dotnet"},
		{"dotnet watch run", "dotnet"},
		// runner prefixes
		{"npx vite", "vite"},
		{"npx --yes vite", "vite"},
		{"bunx vite", "vite"},
		{"pnpm exec vite", "vite"},
		{"uv run uvicorn app:app", "uvicorn"},
		{"poetry run flask run", "flask"},
		{"nohup npm run dev", "npm-script"},
		// env prefixes, backgrounding, chaining
		{"PORT=3000 npm run dev", "npm-script"},
		{"env NODE_ENV=development npm run dev", "npm-script"},
		{"npm run dev &", "npm-script"},
		{"cd frontend && npm run dev", "npm-script"},
		{"npm install && npm run dev", "npm-script"},
		{"./node_modules/.bin/vite", "vite"},
	}
	for _, c := range cases {
		m, ok := Detect(c.cmd)
		if !ok {
			t.Errorf("Detect(%q) = no match, want rule %q", c.cmd, c.rule)
			continue
		}
		if m.Rule != c.rule {
			t.Errorf("Detect(%q) rule = %q, want %q", c.cmd, m.Rule, c.rule)
		}
	}
}

func TestDetectNegatives(t *testing.T) {
	cases := []string{
		"",
		"ls -la",
		"npm install",
		"npm ci",
		"npm run build",
		"npm test",
		"npm run lint",
		"pnpm install",
		"yarn add react",
		"bun install",
		"vite build",
		"next build",
		"nuxt build",
		"astro build",
		"go build ./...",
		"go test ./...",
		"go vet ./...",
		"cargo build",
		"cargo test",
		"dotnet build",
		"php artisan migrate",
		"rails db:migrate",
		"flask db upgrade",
		"python -m pytest",
		"python manage.py migrate",
		"git commit -m \"npm run dev\"",
		"echo npm run dev",
		"grep -r 'npm run dev' .",
		"cat package.json | grep dev",
		"uv run pytest",
		"npx prettier --write .",
		"docker compose up -d",
		"make build",
		// already managed
		"sonar start -- npm run dev",
		"sonar start --group web -- vite",
		"cd app && sonar start -- pnpm dev",
	}
	for _, c := range cases {
		if m, ok := Detect(c); ok {
			t.Errorf("Detect(%q) matched rule %q segment %q, want no match", c, m.Rule, m.Segment)
		}
	}
}

func TestDetectSuggestion(t *testing.T) {
	m, ok := Detect("cd frontend && npm run dev &")
	if !ok {
		t.Fatal("expected a match")
	}
	if m.Segment != "npm run dev" {
		t.Errorf("Segment = %q, want %q", m.Segment, "npm run dev")
	}
	if m.Suggest != "sonar start -- npm run dev" {
		t.Errorf("Suggest = %q, want %q", m.Suggest, "sonar start -- npm run dev")
	}
}

func TestSplitSegmentsRespectsQuotes(t *testing.T) {
	got := splitSegments(`echo "a && b" && npm run dev`)
	want := []string{`echo "a && b"`, "npm run dev"}
	if len(got) != len(want) {
		t.Fatalf("splitSegments = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("segment %d = %q, want %q", i, got[i], want[i])
		}
	}
}

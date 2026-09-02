package cmd

import (
	"strings"
	"testing"
)

func TestSelectedInstallClient(t *testing.T) {
	cases := []struct {
		name    string
		claude  bool
		cursor  bool
		codex   bool
		generic bool
		want    string
		wantErr string
	}{
		{name: "claude", claude: true, want: "claude-code"},
		{name: "cursor", cursor: true, want: "cursor"},
		{name: "codex", codex: true, want: "codex"},
		{name: "generic", generic: true, want: "generic"},
		{name: "none", wantErr: "pick one"},
		{name: "two", claude: true, cursor: true, wantErr: "only one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectedInstallClient(tc.claude, tc.cursor, tc.codex, tc.generic)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("got %q, want error containing %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInstallCommandIsRegistered(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Name() != "install" {
			continue
		}
		found = true
		var hasMCP bool
		for _, sub := range c.Commands() {
			if sub.Name() == "mcp" {
				hasMCP = true
			}
		}
		if !hasMCP {
			t.Error("install command has no mcp subcommand")
		}
		for _, flag := range []string{"claude-code", "cursor", "codex", "generic", "scope", "print", "uninstall", "force"} {
			if c.PersistentFlags().Lookup(flag) == nil {
				t.Errorf("install is missing persistent flag --%s", flag)
			}
		}
	}
	if !found {
		t.Fatal("install command is not registered on rootCmd")
	}
}

package relay

import (
	"strings"
	"testing"
	"time"
)

const goodID = "8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c"

func baseBatch() Batch {
	return Batch{
		InstallID:     goodID,
		AppVersion:    "v0.6.0",
		DaemonVersion: "v0.6.0",
		OS:            "darwin",
		Arch:          "arm64",
		Events:        []Event{{Name: "app_open"}},
	}
}

var testNow = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

func TestValidateBatchAcceptsAWellFormedBatch(t *testing.T) {
	b := baseBatch()
	b.Events[0].Props = map[string]any{
		"pane":    "ports",
		"count":   float64(3),
		"grouped": true,
		"missing": nil,
	}
	if err := ValidateBatch(&b, testNow); err != nil {
		t.Fatalf("valid batch rejected: %v", err)
	}
	if got := b.Events[0].PropsJSON(); !strings.Contains(got, `"pane":"ports"`) {
		t.Fatalf("props were not canonicalised: %s", got)
	}
}

func TestValidateBatchRejects(t *testing.T) {
	long := strings.Repeat("x", 65)

	tests := []struct {
		name string
		fix  func(*Batch)
		code string
	}{
		{"uppercase install id", func(b *Batch) { b.InstallID = strings.ToUpper(goodID) }, "invalid_install_id"},
		{"braced install id", func(b *Batch) { b.InstallID = "{" + goodID + "}" }, "invalid_install_id"},
		{"urn install id", func(b *Batch) { b.InstallID = "urn:uuid:" + goodID }, "invalid_install_id"},
		{"empty install id", func(b *Batch) { b.InstallID = "" }, "invalid_install_id"},
		{"short install id", func(b *Batch) { b.InstallID = "8f14e45f" }, "invalid_install_id"},
		{"unknown os", func(b *Batch) { b.OS = "beos" }, "invalid_os"},
		{"unknown arch", func(b *Batch) { b.Arch = "vax" }, "invalid_arch"},
		{"long version", func(b *Batch) { b.AppVersion = long }, "invalid_version"},
		{"version with a slash", func(b *Batch) { b.DaemonVersion = "v1/2" }, "invalid_version"},
		{"too many events", func(b *Batch) {
			b.Events = make([]Event, MaxEventsPerBatch+1)
			for i := range b.Events {
				b.Events[i] = Event{Name: "app_open"}
			}
		}, "too_many_events"},
		{"unknown event name", func(b *Batch) { b.Events[0].Name = "sniffed_the_disk" }, "unknown_event"},
		{"empty event name", func(b *Batch) { b.Events[0].Name = "" }, "unknown_event"},
		{"unparsable at", func(b *Batch) { b.Events[0].At = "yesterday" }, "invalid_at"},
		{"at far in the future", func(b *Batch) {
			b.Events[0].At = testNow.Add(48 * time.Hour).Format(time.RFC3339)
		}, "invalid_at"},
		{"too many props", func(b *Batch) {
			p := map[string]any{}
			for i := 0; i <= MaxProps; i++ {
				p["k"+string(rune('a'+i%26))+string(rune('a'+i/26))] = true
			}
			b.Events[0].Props = p
		}, "too_many_props"},
		{"uppercase prop key", func(b *Batch) { b.Events[0].Props = map[string]any{"Pane": "ports"} }, "invalid_prop_key"},
		{"prop key starting with a digit", func(b *Batch) { b.Events[0].Props = map[string]any{"1st": "a"} }, "invalid_prop_key"},
		{"long prop value", func(b *Batch) { b.Events[0].Props = map[string]any{"pane": long} }, "prop_too_long"},
		{"nested object prop", func(b *Batch) {
			b.Events[0].Props = map[string]any{"pane": map[string]any{"a": 1}}
		}, "invalid_prop"},
		{"array prop", func(b *Batch) { b.Events[0].Props = map[string]any{"pane": []any{1, 2}} }, "invalid_prop"},
		{"url prop", func(b *Batch) { b.Events[0].Props = map[string]any{"pane": "https://example.com"} }, "unsafe_prop"},
		{"absolute path prop", func(b *Batch) { b.Events[0].Props = map[string]any{"pane": "/Users/ada/code"} }, "unsafe_prop"},
		{"relative path prop", func(b *Batch) { b.Events[0].Props = map[string]any{"pane": "./src"} }, "unsafe_prop"},
		{"home path prop", func(b *Batch) { b.Events[0].Props = map[string]any{"pane": "~/code"} }, "unsafe_prop"},
		{"windows path prop", func(b *Batch) { b.Events[0].Props = map[string]any{"pane": `C:\Users\ada`} }, "unsafe_prop"},
		{"email prop", func(b *Batch) { b.Events[0].Props = map[string]any{"pane": "ada@example.com"} }, "unsafe_prop"},
		{"hostname prop", func(b *Batch) { b.Events[0].Props = map[string]any{"pane": "db.internal.acme.com"} }, "unsafe_prop"},
		{"dotted local hostname prop", func(b *Batch) { b.Events[0].Props = map[string]any{"pane": "sonar.local"} }, "unsafe_prop"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := baseBatch()
			tc.fix(&b)
			err := ValidateBatch(&b, testNow)
			if err == nil {
				t.Fatalf("expected %s, batch was accepted", tc.code)
			}
			if err.Code != tc.code {
				t.Fatalf("got %s (%s), want %s", err.Code, err.Reason, tc.code)
			}
			if strings.TrimSpace(err.Reason) == "" {
				t.Fatal("a rejection must carry a reason")
			}
		})
	}
}

func TestValidateBatchAcceptsHarmlessStrings(t *testing.T) {
	// The unsafe-prop rules have to leave the values telemetry is actually
	// made of alone: versions, enum labels and short identifiers.
	for _, v := range []string{"v0.6.0", "ports", "darwin", "arm64", "group-up", "1.2.3-beta+build", "en_GB", "3"} {
		if why := ValidPropValue(v); why != "" {
			t.Errorf("ValidPropValue(%q) = %q, want it accepted", v, why)
		}
	}
}

func TestValidateBatchAcceptsAnEmptyEventList(t *testing.T) {
	b := baseBatch()
	b.Events = nil
	if err := ValidateBatch(&b, testNow); err != nil {
		t.Fatalf("an empty batch is a legal no-op, got %v", err)
	}
}

func TestValidateBatchAcceptsExactlyTheLimit(t *testing.T) {
	b := baseBatch()
	b.Events = make([]Event, MaxEventsPerBatch)
	for i := range b.Events {
		b.Events[i] = Event{Name: "scan"}
	}
	if err := ValidateBatch(&b, testNow); err != nil {
		t.Fatalf("%d events is the limit, not over it: %v", MaxEventsPerBatch, err)
	}
}

func TestNormalizeProject(t *testing.T) {
	if p, err := NormalizeProject("  "); err != nil || p != DefaultProject {
		t.Fatalf("blank project = %q, %v; want %q, nil", p, err, DefaultProject)
	}
	if p, err := NormalizeProject("acme-eu"); err != nil || p != "acme-eu" {
		t.Fatalf("acme-eu = %q, %v", p, err)
	}
	for _, bad := range []string{"Acme", "-acme", "acme_eu", strings.Repeat("a", MaxProjectLen+1)} {
		if _, err := NormalizeProject(bad); err == nil {
			t.Errorf("project %q was accepted", bad)
		}
	}
}

// The five generic names from the milestone 5 telemetry contract, with the
// props the desktop actually sends. If one of these stops validating, the
// desktop client is broken and nothing else will say so.
func TestValidateBatchAcceptsTheGenericContractEvents(t *testing.T) {
	tests := []struct {
		name  string
		props map[string]any
	}{
		{"onboarding_step", map[string]any{"step": "agents", "outcome": "skipped"}},
		{"view", map[string]any{"name": "ports"}},
		{"action", map[string]any{"name": "kill", "host_kind": "remote", "outcome": "ok"}},
		{"settings_change", map[string]any{"key": "telemetry_enabled"}},
		{"interest", map[string]any{"surface": "account"}},
		{"error", map[string]any{"where": "onboarding", "code": "daemon_unreachable"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := baseBatch()
			b.Events = []Event{{Name: tc.name, Props: tc.props}}
			if err := ValidateBatch(&b, testNow); err != nil {
				t.Fatalf("%s %v was rejected: %v", tc.name, tc.props, err)
			}
		})
	}
}

func TestEventNamesCoverTheMilestone5Contract(t *testing.T) {
	// The contract's own list, verbatim. It is a subset, not the whole set:
	// the CLI and daemon add specific names on top.
	for _, n := range []string{
		"app_open", "app_close", "onboarding_step", "view",
		"action", "error", "settings_change", "interest",
	} {
		if !KnownEventName(n) {
			t.Errorf("the telemetry contract names %q and the relay refuses it", n)
		}
	}
}

func TestEventNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range EventNames {
		if seen[n] {
			t.Errorf("event name %q is listed twice", n)
		}
		seen[n] = true
	}
}

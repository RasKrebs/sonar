package relay

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// MaxBodyBytes is the hard cap on a POST /v1/events body. Anything larger is
// refused with 413 before a byte of it is parsed.
const MaxBodyBytes = 256 << 10

// MaxEventsPerBatch is the largest batch the relay accepts in one request.
const MaxEventsPerBatch = 500

// Prop limits. A property is a flat key/value pair with a short, boring value:
// enough to answer "how many people use groups" and not enough to identify
// anybody.
const (
	MaxProps        = 20
	MaxPropKeyLen   = 40
	MaxPropValueLen = 64
	MaxVersionLen   = 64
	MaxProjectLen   = 40
)

// MaxClockSkew is how far into the future an event's `at` may sit before the
// batch is rejected. A wrong clock is a bug worth surfacing; silently clamping
// it would poison the daily rollups instead.
const MaxClockSkew = 24 * time.Hour

// Batch is the body of POST /v1/events — the telemetry wire format, and the
// only place it is written down.
//
//	{
//	  "install_id": "8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c",
//	  "app_version": "v0.6.0",
//	  "daemon_version": "v0.6.0",
//	  "os": "darwin",
//	  "arch": "arm64",
//	  "events": [
//	    {"name": "app_open", "at": "2026-09-06T09:12:00Z", "props": {"pane": "ports"}}
//	  ]
//	}
//
// Unknown fields are ignored rather than rejected, at both levels: a newer
// client must keep working against an older relay, which is the whole point of
// running the relay as a separate release train.
type Batch struct {
	InstallID     string  `json:"install_id"`
	AppVersion    string  `json:"app_version,omitempty"`
	DaemonVersion string  `json:"daemon_version,omitempty"`
	OS            string  `json:"os,omitempty"`
	Arch          string  `json:"arch,omitempty"`
	Events        []Event `json:"events"`
}

// Event is one thing that happened. `at` is when it happened on the client;
// the relay stamps its own received_at and never trusts `at` for retention.
type Event struct {
	Name  string         `json:"name"`
	At    string         `json:"at,omitempty"`
	Props map[string]any `json:"props,omitempty"`
	// raw keeps the props exactly as they will be stored, filled by
	// ValidateBatch so handlers do not re-marshal.
	raw json.RawMessage `json:"-"`
}

// PropsJSON is the canonical JSON for this event's props, "{}" when there are
// none. Only valid after ValidateBatch has seen the event.
func (e Event) PropsJSON() string {
	if len(e.raw) == 0 {
		return "{}"
	}
	return string(e.raw)
}

// EventNames is the closed set of names the relay accepts. A name that is not
// on this list is a 400, not a silently dropped row: an unknown name means the
// client and the relay disagree about the contract, and failing loudly is how
// that gets noticed.
//
// Adding a name is a relay release followed by a client release, in that
// order. Removing one is never done — an old install keeps sending it.
var EventNames = []string{
	"app_open",
	"app_close",
	"app_update",
	"daemon_start",
	"daemon_stop",
	"scan",
	"port_killed",
	"service_start",
	"service_stop",
	"group_up",
	"group_down",
	"rename",
	"assign",
	"map_created",
	"remote_add",
	"remote_install",
	"host_switch",
	"mcp_tool_call",
	"hook_fired",
	"skill_installed",
	"update_check",
	"update_applied",
	"expose_start",
	"expose_stop",
	"sign_in",
	"error",
}

var eventNameSet = func() map[string]bool {
	m := make(map[string]bool, len(EventNames))
	for _, n := range EventNames {
		m[n] = true
	}
	return m
}()

// KnownEventName reports whether name is in EventNames.
func KnownEventName(name string) bool { return eventNameSet[name] }

// OSNames and ArchNames are the values `os` and `arch` may carry. They are
// Go's own GOOS/GOARCH spellings, because that is what the client has to hand.
var (
	OSNames   = []string{"darwin", "linux", "windows", "freebsd", "openbsd", "netbsd", "dragonfly", "solaris"}
	ArchNames = []string{"amd64", "arm64", "arm", "386", "riscv64", "loong64", "ppc64le", "s390x"}
)

func inList(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Error is a validation failure: a short machine code and a sentence that says
// which field broke which rule. It is the body of every 4xx the relay returns.
type Error struct {
	Code   string `json:"error"`
	Reason string `json:"reason"`
	status int
}

func (e *Error) Error() string { return e.Code + ": " + e.Reason }

// Status is the HTTP status this error should be served as.
func (e *Error) Status() int { return e.status }

func badRequest(code, format string, args ...any) *Error {
	return &Error{Code: code, Reason: fmt.Sprintf(format, args...), status: 400}
}

var (
	// A canonical, lowercase, dash-separated v4-shaped UUID and nothing else:
	// no braces, no urn: prefix, no uppercase. One spelling means install_id
	// is a primary key that cannot alias itself.
	installIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	propKeyRe   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	versionRe   = regexp.MustCompile(`^[A-Za-z0-9._+-]*$`)
	// ProjectRe is the shape of the X-Sonar-Project header.
	projectRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	// A dotted name whose last label is two or more ASCII letters: the shape
	// of a hostname or a domain. "v0.6.0" does not match (last label "0"),
	// "sonar.local" and "db.internal.acme.com" do.
	hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\.[a-z]{2,}$`)
	windowsRe  = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

// ValidPropValue reports why a string property may not be sent, or "" when it
// may be. The rule is blunt on purpose: telemetry carries counts and short
// enum-ish labels, never anything that could be a path, a host or a URL, so
// the relay refuses the *shapes* those take rather than trying to decide
// whether a particular one is sensitive.
//
// The desktop client (5B.5) implements exactly this function, so a value that
// the relay would refuse never leaves the machine in the first place.
func ValidPropValue(v string) string {
	switch {
	case strings.Contains(v, "://"):
		return "looks like a URL"
	case strings.ContainsAny(v, `/\`):
		return "looks like a filesystem path"
	case strings.HasPrefix(v, "~"):
		return "looks like a home-relative path"
	case windowsRe.MatchString(v):
		return "looks like a Windows path"
	case strings.Contains(v, "@"):
		return "looks like an address or user@host"
	case hostnameRe.MatchString(strings.ToLower(v)):
		return "looks like a hostname"
	}
	return ""
}

// ValidateBatch checks every rule the relay enforces and returns the first
// failure, with the JSON path of the offending field in the reason. It also
// normalises: `at` defaults to now, props are canonicalised into the stored
// JSON, and the batch's own strings are trimmed.
//
// The rules, in the order they are applied:
//
//  1. install_id is a canonical lowercase UUID;
//  2. os and arch, when present, are known GOOS/GOARCH spellings;
//  3. app_version and daemon_version are at most 64 chars of [A-Za-z0-9._+-];
//  4. events holds at most 500 entries (an empty list is a legal no-op);
//  5. every name is in EventNames;
//  6. `at`, when present, is RFC 3339 and no more than 24 h in the future;
//  7. props is a flat object of at most 20 keys;
//  8. a key is 1–40 chars of ^[a-z][a-z0-9_]*$;
//  9. a value is a string, a finite number, a bool or null — never an object
//     or an array;
//  10. a string value is at most 64 chars and passes ValidPropValue.
func ValidateBatch(b *Batch, now time.Time) *Error {
	b.InstallID = strings.TrimSpace(b.InstallID)
	if !installIDRe.MatchString(b.InstallID) {
		return badRequest("invalid_install_id",
			"install_id must be a canonical lowercase UUID, got %q", trunc(b.InstallID))
	}
	b.OS = strings.TrimSpace(b.OS)
	if b.OS != "" && !inList(OSNames, b.OS) {
		return badRequest("invalid_os", "os %q is not a known GOOS", trunc(b.OS))
	}
	b.Arch = strings.TrimSpace(b.Arch)
	if b.Arch != "" && !inList(ArchNames, b.Arch) {
		return badRequest("invalid_arch", "arch %q is not a known GOARCH", trunc(b.Arch))
	}
	for _, f := range []struct{ field, v string }{
		{"app_version", b.AppVersion},
		{"daemon_version", b.DaemonVersion},
	} {
		field, v := f.field, f.v
		if len(v) > MaxVersionLen {
			return badRequest("invalid_version", "%s is longer than %d characters", field, MaxVersionLen)
		}
		if !versionRe.MatchString(v) {
			return badRequest("invalid_version",
				"%s may only hold letters, digits, dot, underscore, plus and dash", field)
		}
	}
	if len(b.Events) > MaxEventsPerBatch {
		return badRequest("too_many_events",
			"a batch holds at most %d events, got %d", MaxEventsPerBatch, len(b.Events))
	}
	for i := range b.Events {
		if err := validateEvent(&b.Events[i], i, now); err != nil {
			return err
		}
	}
	return nil
}

func validateEvent(e *Event, i int, now time.Time) *Error {
	e.Name = strings.TrimSpace(e.Name)
	if !KnownEventName(e.Name) {
		return badRequest("unknown_event",
			"events[%d].name %q is not an event this relay accepts", i, trunc(e.Name))
	}
	if at := strings.TrimSpace(e.At); at != "" {
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return badRequest("invalid_at", "events[%d].at is not an RFC 3339 timestamp", i)
		}
		if t.After(now.Add(MaxClockSkew)) {
			return badRequest("invalid_at",
				"events[%d].at is more than %s in the future", i, MaxClockSkew)
		}
	}
	if len(e.Props) > MaxProps {
		return badRequest("too_many_props",
			"events[%d].props holds at most %d keys, got %d", i, MaxProps, len(e.Props))
	}
	for k, v := range e.Props {
		if len(k) > MaxPropKeyLen || !propKeyRe.MatchString(k) {
			return badRequest("invalid_prop_key",
				"events[%d].props key %q must be 1-%d characters of [a-z][a-z0-9_]*",
				i, trunc(k), MaxPropKeyLen)
		}
		switch tv := v.(type) {
		case nil, bool:
		case float64:
			if math.IsNaN(tv) || math.IsInf(tv, 0) {
				return badRequest("invalid_prop",
					"events[%d].props.%s must be a finite number", i, k)
			}
		case json.Number:
			if _, err := tv.Float64(); err != nil {
				return badRequest("invalid_prop",
					"events[%d].props.%s must be a finite number", i, k)
			}
		case string:
			if len(tv) > MaxPropValueLen {
				return badRequest("prop_too_long",
					"events[%d].props.%s is longer than %d characters", i, k, MaxPropValueLen)
			}
			if why := ValidPropValue(tv); why != "" {
				return badRequest("unsafe_prop",
					"events[%d].props.%s %s and may not be sent", i, k, why)
			}
		default:
			return badRequest("invalid_prop",
				"events[%d].props.%s must be a string, number, bool or null — props are flat", i, k)
		}
	}
	raw, err := json.Marshal(orEmpty(e.Props))
	if err != nil {
		return badRequest("invalid_prop", "events[%d].props cannot be encoded: %v", i, err)
	}
	e.raw = raw
	return nil
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// trunc keeps a rejected value out of the error message beyond the point where
// it stops being diagnostic — the reason is logged and returned to a client we
// do not control.
func trunc(s string) string {
	const max = 48
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// NormalizeProject validates the X-Sonar-Project header. An empty header is
// the default project; a malformed one is a 400 rather than a silent fallback,
// so a typo in a self-hosted deployment surfaces immediately.
func NormalizeProject(v string) (string, *Error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultProject, nil
	}
	if len(v) > MaxProjectLen || !projectRe.MatchString(v) {
		return "", badRequest("invalid_project",
			"a project is 1-%d characters of [a-z0-9][a-z0-9-]*", MaxProjectLen)
	}
	return v, nil
}

// DefaultProject is the project a request without X-Sonar-Project belongs to.
const DefaultProject = "default"

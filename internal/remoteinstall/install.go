package remoteinstall

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"
)

// Options is one `sonar remote install` run.
type Options struct {
	// Target is the ssh destination, verbatim ("deploy@203.0.113.7", or a
	// Host alias from ~/.ssh/config).
	Target string
	// Name is the host name to report the install under. Empty means one
	// derived from Target.
	Name string
	// Version is the release to install ("v0.6.0"). Empty means the version of
	// the binary running the install, so local and remote match.
	Version string
	// Identity is an optional ssh key file (-i).
	Identity string
	// SSHArgs are extra flags handed to ssh, ahead of the destination.
	SSHArgs []string
	// NoService installs the binary and starts nothing.
	NoService bool
	// Batch forbids ssh from prompting. The daemon sets it; the CLI leaves it
	// off when it has a terminal, so a passphrase prompt still works.
	Batch bool
	// Resolver finds the asset URL and its published sha256. Nil means GitHub.
	Resolver AssetResolver
}

// Result is what an install ended with, and the payload of the stream's end.
type Result struct {
	Name    string
	Target  string
	Version string
	OS      string
	Arch    string
	BinPath string
	// Service is how the daemon was started: "systemd", "detached", or "none"
	// for --no-service.
	Service string
	// RemoteVersion is what `sonar version` printed on the far end. It should
	// equal Version, and saying so is the point of the check.
	RemoteVersion string
	DaemonRunning bool
	DaemonPID     int
	// LingerHint is set when the systemd user session would end at logout, and
	// carries the `loginctl enable-linger` command that prevents it.
	LingerHint string
}

// Progress receives one line per step as the install runs. Both the CLI's
// printed lines and the daemon's `remote.install` stream chunks are this.
type Progress func(step, detail string)

// Install puts sonar on the remote host and starts it.
//
// The whole download happens on the far end (spec, decision 2): this side only
// resolves which release asset to fetch and what its sha256 must be, writes a
// shell script that says so, and pipes it to `sh -s` over one ssh session. Two
// short sessions bracket it: `uname -s -m` before, to pick the asset, and
// `sonar version` plus `sonar daemon status` after, to prove it worked.
//
// Re-running upgrades in place: the binary is replaced atomically and the
// service restarted, so an install and an update are the same operation.
func Install(ctx context.Context, opts Options, progress Progress) (*Result, error) {
	if progress == nil {
		progress = func(string, string) {}
	}
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		return nil, fmt.Errorf("a target is required, as user@host")
	}

	version, err := ResolveVersion(opts.Version)
	if err != nil {
		return nil, err
	}

	r := runner{target: target, identity: opts.Identity, extra: opts.SSHArgs, batch: opts.Batch}

	progress(StepConnect, target)
	uname, err := r.output(ctx, "uname -s -m")
	if err != nil {
		return nil, err
	}
	platform, err := ParsePlatform(uname)
	if err != nil {
		return nil, err
	}
	progress(StepDetect, platform.String())

	resolve := opts.Resolver
	if resolve == nil {
		resolve = GitHubResolver
	}
	url, sha, err := resolve(ctx, version, platform.Asset())
	if err != nil {
		return nil, err
	}
	progress(StepResolve, version+" "+platform.Asset())

	res := &Result{
		Name:    hostName(opts.Name, target),
		Target:  target,
		Version: version,
		OS:      platform.OS,
		Arch:    platform.Arch,
		BinPath: BinDisplay,
		Service: ServiceNone,
	}

	script := Script(scriptParams{
		Version:  version,
		Platform: platform.String(),
		Asset:    platform.Asset(),
		URL:      url,
		SHA256:   sha,
		Service:  !opts.NoService,
	})

	err = r.stream(ctx, "sh -s", strings.NewReader(script), func(line string) {
		step, detail := parseMarker(line)
		switch step {
		case StepService:
			res.Service = detail
		case StepLinger:
			res.LingerHint = detail
		}
		progress(step, detail)
	})
	if err != nil {
		return nil, err
	}

	if err := check(ctx, r, res, progress); err != nil {
		return nil, err
	}
	progress(StepDone, res.RemoteVersion+" at "+res.BinPath)
	return res, nil
}

// verifyCommand asks the freshly installed binary what it is and whether its
// daemon is up. `|| true` keeps a stopped daemon — which is the expected answer
// after --no-service — from looking like an ssh failure.
var verifyCommand = `"` + BinPath + `" version 2>&1 && ` +
	`"` + BinPath + `" daemon status --json 2>/dev/null || true`

// checkAttempts and checkBackoff bound the wait for a daemon that has been
// started but has not bound its socket yet: systemctl returns as soon as it has
// forked the unit, so the first status can legitimately be "not running".
const (
	checkAttempts = 4
	checkBackoff  = 750 * time.Millisecond
)

type daemonStatus struct {
	Running       bool   `json:"running"`
	PID           int    `json:"pid"`
	DaemonVersion string `json:"daemon_version"`
}

// check runs the post-install verification and fills in the result.
//
// When step 3A.2's `sonar daemon stdio` lands this becomes a `daemon.hello`
// over the bridge, which proves the protocol as well as the process. Until
// then `daemon status` over a second ssh session is the same answer one hop
// further out.
func check(ctx context.Context, r runner, res *Result, progress Progress) error {
	progress(StepCheck, res.BinPath)

	for attempt := 0; attempt < checkAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(checkBackoff):
			}
		}
		out, err := r.output(ctx, verifyCommand)
		if err != nil {
			return err
		}

		res.RemoteVersion = parseVersionLine(out)
		if res.RemoteVersion == "" {
			return fmt.Errorf("installed %s on %s, but `%s version` printed nothing usable: %s",
				res.Version, res.Target, res.BinPath, strings.TrimSpace(firstLine(out)))
		}
		if res.RemoteVersion != res.Version {
			return fmt.Errorf("installed %s on %s, but the binary there reports %s",
				res.Version, res.Target, res.RemoteVersion)
		}

		if status, ok := parseDaemonStatus(out); ok {
			res.DaemonRunning = status.Running
			res.DaemonPID = status.PID
		}
		if res.DaemonRunning || res.Service == ServiceNone {
			break
		}
	}

	if res.Service != ServiceNone && !res.DaemonRunning {
		return fmt.Errorf("installed %s on %s, but its daemon is not running\nhint: `ssh %s %s daemon log` says why",
			res.Version, res.Target, res.Target, res.BinPath)
	}
	return nil
}

// versionLine matches `sonar version`'s output: "sonar v0.6.0 (linux/amd64)".
var versionLine = regexp.MustCompile(`^sonar\s+(\S+)\s`)

func parseVersionLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if m := versionLine.FindStringSubmatch(strings.TrimSpace(line) + " "); m != nil {
			return m[1]
		}
	}
	return ""
}

// parseDaemonStatus reads the JSON object `daemon status --json` prints, which
// follows the version line on the same stream.
func parseDaemonStatus(out string) (daemonStatus, bool) {
	i := strings.Index(out, "{")
	if i < 0 {
		return daemonStatus{}, false
	}
	var s daemonStatus
	if err := json.Unmarshal([]byte(out[i:]), &s); err != nil {
		return daemonStatus{}, false
	}
	return s, true
}

// parseMarker splits one line of the remote script's stdout into a step and a
// detail. A line the script did not tag is still worth showing — it is the
// remote's own output — so it becomes a chunk with step "remote".
func parseMarker(line string) (string, string) {
	rest, ok := strings.CutPrefix(line, Marker+"\t")
	if !ok {
		return StepRemote, strings.TrimSpace(line)
	}
	step, detail, _ := strings.Cut(rest, "\t")
	return strings.TrimSpace(step), strings.TrimSpace(detail)
}

// scanLines hands every line of r to onLine. A single line longer than
// bufio's default is split rather than dropped: progress must not stop because
// a remote printed something enormous.
func scanLines(r io.Reader, onLine func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		onLine(line)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

var nameUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// hostName is the name the install reports the host under: the one the caller
// gave, or one derived from the ssh destination. Host names are [a-z0-9-]+
// (spec, "CLI"), so a domain contributes its first label and an address its
// digits.
func hostName(given, target string) string {
	if n := sanitizeName(given); n != "" {
		return n
	}
	host := target
	if _, after, ok := strings.Cut(host, "@"); ok {
		host = after
	}
	host = strings.Trim(host, "[]")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if net.ParseIP(host) == nil {
		host, _, _ = strings.Cut(host, ".")
	}
	if n := sanitizeName(host); n != "" {
		return n
	}
	return "remote"
}

func sanitizeName(s string) string {
	n := nameUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	return strings.Trim(n, "-")
}

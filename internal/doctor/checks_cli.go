package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/selfupdate"
)

// checkCLIOnPath answers "is the sonar I just ran the sonar PATH would find?".
// A second install earlier on PATH is the single most confusing thing a user
// can have: every hint the CLI prints then names a binary they are not running.
func checkCLIOnPath(_ context.Context, env *Env) rpc.DoctorCheck {
	exe, err := env.Executable()
	if err != nil {
		return rpc.DoctorCheck{
			Status:  StatusWarn,
			Summary: "could not resolve the running binary",
			Detail:  err.Error(),
		}
	}
	exe = resolveLinks(exe)

	onPath, err := env.LookPath(binaryName(env.GOOS))
	if err != nil {
		return rpc.DoctorCheck{
			Status:  StatusFail,
			Summary: "sonar is not on PATH",
			Detail:  fmt.Sprintf("running %s, but PATH has no sonar: %v", exe, err),
			Fix: fmt.Sprintf("put %s on PATH (add %s to it, or reinstall with the script in the README)",
				binaryName(env.GOOS), filepath.Dir(exe)),
		}
	}
	onPath = resolveLinks(onPath)

	if samePath(env.GOOS, exe, onPath) {
		return rpc.DoctorCheck{
			Status:  StatusOK,
			Summary: "sonar resolves from PATH",
			Detail:  onPath,
		}
	}
	return rpc.DoctorCheck{
		Status:  StatusWarn,
		Summary: "another sonar is first on PATH",
		Detail:  fmt.Sprintf("PATH resolves sonar to %s; you are running %s", onPath, exe),
		Fix: fmt.Sprintf("remove %s, or put %s earlier on PATH",
			onPath, filepath.Dir(exe)),
	}
}

// checkCLIVersionCurrent compares this build with the newest published release.
// It never fails: a pinned old version and a machine with no network are both
// deliberate states, so the worst it reports is a warning.
func checkCLIVersionCurrent(ctx context.Context, env *Env) rpc.DoctorCheck {
	if v := strings.TrimSpace(env.Version); v == "" || v == "dev" {
		return rpc.DoctorCheck{
			Status:  StatusSkip,
			Summary: "development build",
			Detail:  "a binary built from source has no release to compare against",
		}
	}
	latest, err := env.LatestRelease(ctx)
	if err != nil {
		return rpc.DoctorCheck{
			Status:  StatusSkip,
			Summary: "could not reach the release feed",
			Detail:  err.Error(),
		}
	}
	if selfupdate.IsNewer(env.Version, latest) {
		return rpc.DoctorCheck{
			Status:  StatusWarn,
			Summary: fmt.Sprintf("%s is behind %s", env.Version, latest),
			Detail:  fmt.Sprintf("the newest published release is %s", latest),
			Fix:     "sonar update",
		}
	}
	return rpc.DoctorCheck{
		Status:  StatusOK,
		Summary: fmt.Sprintf("%s is current", env.Version),
		Detail:  fmt.Sprintf("newest published release: %s", latest),
	}
}

func binaryName(goos string) string {
	if goos == "windows" {
		return "sonar.exe"
	}
	return "sonar"
}

// resolveLinks follows symlinks when it can and returns the input otherwise:
// two spellings of one file must compare equal, but a path that cannot be
// resolved is still worth printing.
func resolveLinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// samePath compares two resolved paths with the platform's case rules.
func samePath(goos, a, b string) bool {
	if goos == "windows" || goos == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

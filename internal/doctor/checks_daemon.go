package doctor

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/store"
)

func checkDaemonReachable(ctx context.Context, env *Env) rpc.DoctorCheck {
	info := env.Daemon(ctx)
	switch {
	case info.Local:
		return rpc.DoctorCheck{
			Status:  StatusOK,
			Summary: "the daemon is running",
			Detail:  fmt.Sprintf("this answer came from the daemon itself (pid %d, %s)", info.PID, info.Socket),
		}
	case info.Reachable:
		return rpc.DoctorCheck{
			Status:  StatusOK,
			Summary: "the daemon is listening",
			Detail:  fmt.Sprintf("pid %d on %s", info.PID, info.Socket),
		}
	}
	detail := env.SocketPath
	if info.Err != nil {
		detail = fmt.Sprintf("%s: %v", env.SocketPath, info.Err)
	}
	return rpc.DoctorCheck{
		Status:  StatusFail,
		Summary: "no daemon is listening",
		Detail:  detail,
		Fix:     "sonar serve --detach",
		Fixable: true,
	}
}

func checkDaemonVersionMatches(ctx context.Context, env *Env) rpc.DoctorCheck {
	info := env.Daemon(ctx)
	if !info.Reachable {
		return rpc.DoctorCheck{
			Status:  StatusSkip,
			Summary: "no daemon to compare against",
			Detail:  "start the daemon and run doctor again",
		}
	}
	if info.Version == env.Version {
		return rpc.DoctorCheck{
			Status:  StatusOK,
			Summary: fmt.Sprintf("daemon and CLI are both %s", env.Version),
		}
	}
	return rpc.DoctorCheck{
		Status:  StatusWarn,
		Summary: fmt.Sprintf("daemon is %s, CLI is %s", info.Version, env.Version),
		Detail: "an old daemon keeps serving whoever started it; restarting it " +
			"adopts the version of the binary you are running now",
		Fix: "sonar daemon restart",
	}
}

func checkDaemonProtocol(ctx context.Context, env *Env) rpc.DoctorCheck {
	info := env.Daemon(ctx)
	if !info.Reachable {
		return rpc.DoctorCheck{
			Status:  StatusSkip,
			Summary: "no daemon to handshake with",
			Detail:  "start the daemon and run doctor again",
		}
	}
	got, gotErr := protocolMajor(info.ProtocolVersion)
	want, wantErr := protocolMajor(rpc.ProtocolVersion)
	if gotErr != nil || wantErr != nil {
		return rpc.DoctorCheck{
			Status:  StatusFail,
			Summary: "unreadable protocol version",
			Detail:  fmt.Sprintf("daemon reported %q, this build speaks %q", info.ProtocolVersion, rpc.ProtocolVersion),
			Fix:     "sonar daemon restart",
		}
	}
	if got != want {
		return rpc.DoctorCheck{
			Status:  StatusFail,
			Summary: fmt.Sprintf("protocol major %d, this build speaks %d", got, want),
			Detail: fmt.Sprintf("daemon protocol %s, client protocol %s; only the major has to match",
				info.ProtocolVersion, rpc.ProtocolVersion),
			Fix: "sonar daemon restart",
		}
	}
	return rpc.DoctorCheck{
		Status:  StatusOK,
		Summary: fmt.Sprintf("protocol %s", info.ProtocolVersion),
		Detail:  fmt.Sprintf("this build speaks %s; majors match", rpc.ProtocolVersion),
	}
}

func protocolMajor(v string) (int, error) {
	head, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	return strconv.Atoi(head)
}

// checkDBOK opens the database the daemon uses and reports its schema version
// and size. It does not create one: a machine whose daemon has never run has
// no database yet, which is normal rather than broken.
func checkDBOK(_ context.Context, env *Env) rpc.DoctorCheck {
	path := env.DBPath
	if path == "" {
		return rpc.DoctorCheck{Status: StatusSkip, Summary: "no database path resolved"}
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return rpc.DoctorCheck{
			Status:  StatusWarn,
			Summary: "no database yet",
			Detail:  path + " is created the first time the daemon runs",
			Fix:     "sonar serve --detach",
		}
	}
	if err != nil {
		return rpc.DoctorCheck{
			Status:  StatusFail,
			Summary: "cannot stat the database",
			Detail:  err.Error(),
		}
	}

	db, err := store.Open(path)
	if err != nil {
		return rpc.DoctorCheck{
			Status:  StatusFail,
			Summary: "cannot open the database",
			Detail:  fmt.Sprintf("%s: %v", path, err),
			Fix:     "move it aside and let the daemon recreate it: mv " + path + " " + path + ".bad",
		}
	}
	defer db.Close()

	version, err := db.Version()
	if err != nil {
		return rpc.DoctorCheck{
			Status:  StatusFail,
			Summary: "cannot read the schema version",
			Detail:  fmt.Sprintf("%s: %v", path, err),
		}
	}
	latest := store.LatestVersion()
	size := humanBytes(info.Size())
	if version != latest {
		return rpc.DoctorCheck{
			Status:  StatusWarn,
			Summary: fmt.Sprintf("schema v%d, this build expects v%d", version, latest),
			Detail:  fmt.Sprintf("%s (%s)", path, size),
			Fix:     "sonar daemon restart — the daemon migrates on open",
		}
	}
	return rpc.DoctorCheck{
		Status:  StatusOK,
		Summary: fmt.Sprintf("schema v%d, %s", version, size),
		Detail:  path,
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

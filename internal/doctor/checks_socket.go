package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// checkSocketPermissions is contract §21 read back off the disk: the socket
// must be a same-user-only file, and the directory sonar makes for it must be
// 0700. Windows has neither — the pipe's ACL is applied at bind time and there
// is no file to stat — so the check skips rather than inventing an equivalent.
func checkSocketPermissions(ctx context.Context, env *Env) rpc.DoctorCheck {
	socket := env.SocketPath
	if env.GOOS == "windows" || strings.HasPrefix(socket, `\\`) {
		return rpc.DoctorCheck{
			Status:  StatusSkip,
			Summary: "named pipe, not a file",
			Detail: "on Windows the daemon listens on " + daemon.WindowsPipe +
				", whose DACL grants only this user and SYSTEM when it is bound; there are no file modes to check",
		}
	}
	if socket == "" {
		return rpc.DoctorCheck{Status: StatusSkip, Summary: "no socket path resolved"}
	}

	info, err := os.Stat(socket)
	if os.IsNotExist(err) {
		if env.Daemon(ctx).Reachable {
			return rpc.DoctorCheck{
				Status:  StatusFail,
				Summary: "the daemon answers but its socket is gone",
				Detail:  socket + " does not exist",
				Fix:     "sonar daemon restart",
			}
		}
		return rpc.DoctorCheck{
			Status:  StatusSkip,
			Summary: "no socket to check",
			Detail:  socket + " appears when the daemon starts",
		}
	}
	if err != nil {
		return rpc.DoctorCheck{Status: StatusFail, Summary: "cannot stat the socket", Detail: err.Error()}
	}

	var problems []string
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		problems = append(problems, fmt.Sprintf("mode is %04o, want 0600", mode))
	}
	if uid, ok := fileOwner(info); ok && env.UID >= 0 && uid != env.UID {
		problems = append(problems, fmt.Sprintf("owned by uid %d, you are uid %d", uid, env.UID))
	}

	dir := filepath.Dir(socket)
	dirNote := ""
	if daemon.OwnsSocketDir(socket) {
		if dirInfo, err := os.Stat(dir); err == nil {
			if dirMode := dirInfo.Mode().Perm(); dirMode&0o077 != 0 {
				problems = append(problems, fmt.Sprintf("%s is %04o, want 0700", dir, dirMode))
			} else {
				dirNote = fmt.Sprintf(", %s is %04o", dir, dirMode)
			}
		}
	}

	if len(problems) > 0 {
		return rpc.DoctorCheck{
			Status:  StatusFail,
			Summary: "the socket is reachable by other users",
			Detail:  fmt.Sprintf("%s: %s", socket, strings.Join(problems, "; ")),
			Fix:     fmt.Sprintf("chmod 700 %s && chmod 600 %s", dir, socket),
		}
	}
	return rpc.DoctorCheck{
		Status:  StatusOK,
		Summary: fmt.Sprintf("socket is %04o and yours", mode),
		Detail:  socket + dirNote,
	}
}

package doctor

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// `daemon.doctor` exists so the desktop app can diagnose an installation
// without shelling out to a CLI it may not have found yet. It runs the same
// list `sonar doctor` runs and returns the same rows; the checks that are about
// the CLI process come back as `skip` with a detail saying so (contract §8: the
// handler lives here, and the daemon package never imports this one).
func init() {
	daemon.RegisterHandler("daemon.doctor", handleDoctor)
	daemon.RegisterCapability("doctor")
}

func handleDoctor(ctx context.Context, req *daemon.Request) (any, error) {
	var p rpc.DaemonDoctorParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if bad := UnknownSelectors(p.Only); len(bad) > 0 {
		return nil, rpc.NewError(rpc.CodeInvalidParams,
			"unknown check "+strings.Join(bad, ", "),
			"known checks: "+strings.Join(append(IDs(), Prefixes()...), ", "))
	}
	if p.Project != "" && !filepath.IsAbs(p.Project) {
		return nil, rpc.NewError(rpc.CodeInvalidParams,
			"project must be an absolute path",
			"the daemon has its own working directory, so a relative path would mean something else here")
	}

	rt := req.Runtime
	self := DaemonInfo{
		Reachable:       true,
		Local:           true,
		Version:         rt.Version,
		ProtocolVersion: rpc.ProtocolVersion,
		Socket:          rt.Socket,
		PID:             rt.PID,
		DBPath:          rt.DBPath(),
	}
	env := Env{
		Mode:       ModeDaemon,
		Version:    rt.Version,
		Project:    p.Project,
		SocketPath: rt.Socket,
		DBPath:     rt.DBPath(),
		Daemon:     func(context.Context) DaemonInfo { return self },
		Docker:     DockerProbe,
	}
	return Run(ctx, env, p.Only), nil
}

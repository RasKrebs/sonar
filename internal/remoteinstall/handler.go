package remoteinstall

import (
	"context"
	"errors"
	"strings"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// The daemon serves `remote.install` from this package's init(), the way
// groups.start is served from internal/daemon/groupstart: the install shells
// out to ssh and reads GitHub, neither of which belongs in internal/daemon
// (contract §8). Linking this package in is what makes the method exist.
func init() {
	daemon.RegisterHandler("remote.install", handleRemoteInstall)
	daemon.RegisterCapability("remote")
}

func handleRemoteInstall(ctx context.Context, req *daemon.Request) (any, error) {
	var p rpc.RemoteInstallParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Target) == "" {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "target is required",
			"the ssh destination, as user@host or a Host from ~/.ssh/config")
	}

	opts := Options{
		Target:    strings.TrimSpace(p.Target),
		Name:      p.Name,
		Version:   p.Version,
		Identity:  p.Identity,
		SSHArgs:   p.SSHArgs,
		NoService: p.NoService,
		// The daemon has no terminal, so ssh must never sit on a password or
		// passphrase prompt: an install that hangs forever is worse than one
		// that says "load your key into ssh-agent first".
		Batch: true,
	}

	// Fail before the stream opens on anything decidable up front, so the
	// caller gets a plain error rather than a subscription that ends badly.
	if _, err := ResolveVersion(opts.Version); err != nil {
		return nil, installError(err)
	}

	initial := rpc.RemoteInstallResult{MutationResult: rpc.MutationResult{OK: true, Affected: []string{}}}
	return daemon.StartStream(ctx, req, initial, func(ctx context.Context, s *daemon.Stream) (any, error) {
		res, err := Install(ctx, opts, func(step, detail string) {
			_ = s.Send(rpc.RemoteInstallChunk{Step: step, Detail: detail})
		})
		if err != nil {
			return nil, installError(err)
		}
		return End(res), nil
	})
}

// End is the wire form of a finished install.
func End(res *Result) rpc.RemoteInstallEnd {
	return rpc.RemoteInstallEnd{
		Name:          res.Name,
		Target:        res.Target,
		Version:       res.Version,
		OS:            res.OS,
		Arch:          res.Arch,
		BinPath:       res.BinPath,
		Service:       res.Service,
		DaemonRunning: res.DaemonRunning,
		DaemonPID:     res.DaemonPID,
		LingerHint:    res.LingerHint,
	}
}

// installError gives the failures a caller can act on their own code, so the
// desktop can tell "this host cannot run sonar" from "ssh could not connect".
func installError(err error) error {
	var re *rpc.Error
	if errors.As(err, &re) {
		return err
	}
	var unsupported *UnsupportedPlatformError
	if errors.As(err, &unsupported) {
		return rpc.NewError(rpc.CodeUnsupported, unsupported.Error(), "")
	}
	if errors.Is(err, ErrDevBuild) {
		detail, hint, _ := strings.Cut(err.Error(), "\nhint: ")
		return rpc.NewError(rpc.CodeInvalidParams, detail, hint)
	}
	detail, hint, _ := strings.Cut(err.Error(), "\nhint: ")
	return rpc.NewError(rpc.CodeInternal, detail, hint)
}

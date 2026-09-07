package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// launch starts the app that was just installed and returns immediately.
//
// macOS hands the bundle to LaunchServices, which is the only way to start an
// .app correctly: it is the difference between an application with a Dock
// icon and a bare process. Linux has no such service, so the AppImage is
// started in a session of its own with its standard streams detached, which is
// what `setsid` buys — without it the app dies with the terminal that
// installed it, and with inherited streams it scribbles over the shell prompt.
func launch(ctx context.Context, o *Options, path string) error {
	if o.GOOS == "darwin" {
		if _, err := o.Exec(ctx, "open", "-a", path); err != nil {
			return fmt.Errorf("open -a %s: %w", path, err)
		}
		return nil
	}

	cmd := exec.Command(path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		defer devNull.Close()
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	}
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", path, err)
	}
	return cmd.Process.Release()
}

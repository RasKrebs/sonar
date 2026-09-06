package testenv

import (
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

// helperEnv turns this binary into a stand-in child process rather than a test
// run. The leak-gate tests need a process that looks to `ps` exactly like a
// leaked daemon — argv[0] a binary called sonar, argv[1] "serve" — and the
// TMPDIR test needs a child that reports the temp directory it inherited.
const helperEnv = "SONAR_TESTENV_HELPER"

// Helper modes.
const (
	helperTempDir = "tmpdir"
	helperServe   = "serve"
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "":
	case helperTempDir:
		fmt.Print(os.TempDir())
		os.Exit(0)
	case helperServe:
		// Alive until the parent closes our stdin, and never past the suite: a
		// stand-in for a leaked daemon must not turn into one.
		time.AfterFunc(2*time.Minute, func() { os.Exit(0) })
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(Run(m))
}

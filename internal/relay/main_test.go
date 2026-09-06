package relay

import (
	"os"
	"testing"

	"github.com/raskrebs/sonar/internal/testenv"
)

// Every path sonar resolves from the environment points at a private temp
// directory for the whole of this test binary, daemon autostart is off, and
// the run fails if a test leaves a daemon behind (step 1A.20).
//
// The relay never reads that environment — it takes its database from a flag
// and touches nothing under ~/.config/sonar — and the isolation is here to
// prove exactly that: the integration test asserts the directory stays absent.
func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

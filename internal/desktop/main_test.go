package desktop

import (
	"os"
	"testing"

	"github.com/raskrebs/sonar/internal/testenv"
)

// The installer writes to the user's config and reads HOME for every Linux
// path it touches, so this binary gets an isolated HOME like every other.
// Nothing in here may reach /Applications or the real ~/.config/sonar.
func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

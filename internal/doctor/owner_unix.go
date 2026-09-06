//go:build !windows

package doctor

import (
	"os"
	"syscall"
)

// fileOwner reports the numeric owner of a file. The second result is false
// when the platform does not expose one.
func fileOwner(info os.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

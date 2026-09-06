//go:build windows

package doctor

import "os"

// fileOwner has no answer on Windows: the socket is a named pipe there and the
// permission check skips before it is ever called.
func fileOwner(os.FileInfo) (int, bool) { return 0, false }

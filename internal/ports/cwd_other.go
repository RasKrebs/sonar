//go:build !linux && !darwin && !windows

package ports

// batchGetCwds is a no-op on platforms with no per-process cwd lookup of their
// own. Linux reads /proc, macOS asks lsof and Windows reads the PEB; anything
// else leaves Cwd empty, and the cwd-augmentation step in resolveProcessName is
// simply skipped.
func batchGetCwds(pids []int) map[int]string {
	return nil
}

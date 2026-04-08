//go:build !linux && !darwin

package ports

// batchGetCwds is a no-op on platforms without a stable per-process cwd lookup.
// On Windows, the cwd-augmentation step in resolveProcessName is simply skipped.
func batchGetCwds(pids []int) map[int]string {
	return nil
}

// batchGetServiceUnits is a no-op on platforms without systemd / launchd.
func batchGetServiceUnits(pids []int) map[int]string {
	return nil
}

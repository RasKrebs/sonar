//go:build !linux && !darwin

package ports

// batchGetServiceUnits is a no-op on platforms without systemd / launchd.
func batchGetServiceUnits(pids []int) map[int]string {
	return nil
}

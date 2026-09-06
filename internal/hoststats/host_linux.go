package hoststats

import (
	"context"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// readHost reads the machine from /proc and one statfs. Nothing shells out and
// nothing needs cgo, so this is the same code path inside a scratch container
// as on a desktop.
func readHost(_ context.Context) (reading, error) {
	r := reading{diskPath: "/", kernel: unameRelease()}

	if data, err := os.ReadFile("/proc/stat"); err == nil {
		if busy, total, ok := parseProcStat(string(data)); ok {
			r.cpu = cpuSample{ok: true, busy: busy, total: total, cpus: runtime.NumCPU()}
		}
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		if load, ok := parseLoadavg(string(data)); ok {
			r.load = load
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		if used, total, ok := parseMeminfo(string(data)); ok {
			r.memUsed, r.memTotal = i64(used), i64(total)
		}
	}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		if secs, ok := parseUptime(string(data)); ok {
			r.uptimeS = i64(secs)
		}
	}
	r.diskUsed, r.diskTotal = diskUsage(r.diskPath)
	return r, nil
}

// unameRelease is the kernel release, "6.8.0-40-generic".
func unameRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return ""
	}
	return cstring(u.Release[:])
}

func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// diskUsage measures the filesystem holding path. Used is total minus the free
// space *this user* may claim: the reserved blocks root keeps back are not
// space anyone can have, so counting them as free would overstate the disk on
// every default ext4 by five percent.
func diskUsage(path string) (used, total *int64) {
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return nil, nil
	}
	bsize := uint64(fs.Bsize)
	t := fs.Blocks * bsize
	avail := fs.Bavail * bsize
	if t == 0 || avail > t {
		return nil, nil
	}
	return u64(t - avail), u64(t)
}

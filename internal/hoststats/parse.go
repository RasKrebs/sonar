package hoststats

import (
	"strconv"
	"strings"
)

// This file holds the pure parsers, deliberately free of build tags: every
// platform's text and byte formats are decoded here so the tests for all three
// run on whatever machine `go test ./...` happens to be.

// parseProcStat reads Linux /proc/stat and returns the cumulative busy and
// total jiffies of the aggregate "cpu" line.
//
//	cpu  user nice system idle iowait irq softirq steal guest guest_nice
//
// Idle time is idle + iowait: a core waiting on disk is not doing work. guest
// and guest_nice are already counted inside user and nice, so they are dropped
// rather than added twice.
func parseProcStat(data string) (busy, total float64, ok bool) {
	for line := range strings.SplitSeq(data, "\n") {
		if !strings.HasPrefix(line, "cpu ") && !strings.HasPrefix(line, "cpu\t") {
			continue
		}
		fields := strings.Fields(line)[1:]
		if len(fields) < 4 {
			return 0, 0, false
		}
		if len(fields) > 8 {
			fields = fields[:8]
		}
		var sum, idle float64
		for i, f := range fields {
			v, err := strconv.ParseFloat(f, 64)
			if err != nil {
				return 0, 0, false
			}
			sum += v
			if i == 3 || i == 4 { // idle, iowait
				idle += v
			}
		}
		return sum - idle, sum, true
	}
	return 0, 0, false
}

// parseLoadavg reads Linux /proc/loadavg: "0.42 0.51 0.60 1/523 12345".
func parseLoadavg(data string) ([]float64, bool) {
	fields := strings.Fields(data)
	if len(fields) < 3 {
		return nil, false
	}
	out := make([]float64, 3)
	for i := range out {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

// parseUptime reads Linux /proc/uptime: "12345.67 98765.43" (seconds since
// boot, then idle seconds summed over all cores).
func parseUptime(data string) (int64, bool) {
	fields := strings.Fields(data)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return int64(v), true
}

// parseMeminfo reads Linux /proc/meminfo and returns used and total bytes.
//
// "Used" is total minus MemAvailable, the kernel's own estimate of what a new
// workload could claim without swapping. It is the number `free -h` prints as
// "available" and the only one that treats reclaimable page cache honestly. On
// a kernel too old to publish it (< 3.14) the classic free + buffers + cached
// sum stands in.
func parseMeminfo(data string) (used, total int64, ok bool) {
	vals := map[string]int64{}
	for line := range strings.SplitSeq(data, "\n") {
		key, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// Every size in /proc/meminfo is kB; the few unitless counters are
		// not sizes and are never read here.
		vals[key] = v * 1024
	}

	total, ok = vals["MemTotal"]
	if !ok || total <= 0 {
		return 0, 0, false
	}
	available, ok := vals["MemAvailable"]
	if !ok {
		available = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
	}
	if available > total {
		available = total
	}
	return total - available, total, true
}

// parsePSTimes reads the output of `ps -A -o pid=,time=` on macOS, one process
// per line: "  501    40:18.12". The value is cumulative CPU time, so a pair of
// samples gives the work done in between.
func parsePSTimes(data string) (map[int]float64, bool) {
	out := map[int]float64{}
	for line := range strings.SplitSeq(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		secs, ok := parseCPUTime(fields[1])
		if !ok {
			continue
		}
		out[pid] = secs
	}
	return out, len(out) > 0
}

// parseCPUTime decodes ps's cumulative time column: "MM:SS.ss", or
// "HH:MM:SS.ss" / "D-HH:MM:SS" once a process has been running long enough.
func parseCPUTime(s string) (float64, bool) {
	if d, rest, found := strings.Cut(s, "-"); found {
		days, err := strconv.ParseFloat(d, 64)
		if err != nil {
			return 0, false
		}
		rest, ok := parseCPUTime(rest)
		if !ok {
			return 0, false
		}
		return days*86400 + rest, true
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var total float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 {
			return 0, false
		}
		total = total*60 + v
	}
	return total, true
}

// parseSysctlLoadavg decodes darwin's `vm.loadavg`, a struct loadavg:
//
//	fixpt_t ldavg[3]; long fscale;
//
// fscale is read from the tail so the padding before a 64-bit long does not
// have to be assumed, and falls back to the FSCALE the kernel has used since
// 4.4BSD when the struct is short or the scale is zero.
func parseSysctlLoadavg(raw []byte) ([]float64, bool) {
	if len(raw) < 12 {
		return nil, false
	}
	scale := float64(2048)
	if len(raw) >= 20 {
		if v := float64(le32(raw[len(raw)-8:])); v > 0 {
			scale = v
		}
	} else if len(raw) >= 16 {
		if v := float64(le32(raw[12:])); v > 0 {
			scale = v
		}
	}
	out := make([]float64, 3)
	for i := range out {
		out[i] = float64(le32(raw[i*4:])) / scale
	}
	return out, true
}

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func le64(b []byte) uint64 {
	return uint64(le32(b)) | uint64(le32(b[4:]))<<32
}

// filetime folds a Windows FILETIME's two halves into the 100-nanosecond
// counter they encode. GetSystemTimes hands back three of them.
func filetime(high, low uint32) uint64 {
	return uint64(high)<<32 | uint64(low)
}

// windowsCPU turns GetSystemTimes' three counters into the cumulative busy and
// total this collector compares between ticks. The kernel counter already
// includes the idle time, so total is kernel + user and busy is what is left
// once idle is taken out.
func windowsCPU(idle, kernel, user uint64) (busy, total float64, ok bool) {
	t := kernel + user
	if t == 0 || idle > t {
		return 0, 0, false
	}
	return float64(t - idle), float64(t), true
}

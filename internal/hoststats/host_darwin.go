package hoststats

import (
	"context"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// readHost reads the machine from sysctl, one statfs and one `ps`.
//
// macOS has no cgo-free system-wide CPU counter: the numbers Activity Monitor
// shows come from host_processor_info, a Mach call that needs libSystem, and
// the BSD sysctls that carry them elsewhere (kern.cp_time) do not exist on
// XNU. kern.proc.all's per-process tick fields are present in the struct but
// left at zero by the kernel. What is left is `ps -A -o pid=,time=`: every
// process's cumulative CPU time, ~20 ms for one fork, differenced per pid
// across the scan interval and divided by wall time times hw.ncpu. Work done
// by a process that exited between two ticks is lost, so the figure runs a
// little low on a machine churning through short-lived processes.
func readHost(ctx context.Context) (reading, error) {
	r := reading{diskPath: "/"}
	r.kernel, _ = unix.Sysctl("kern.osrelease")

	if load, err := unix.SysctlRaw("vm.loadavg"); err == nil {
		if parsed, ok := parseSysctlLoadavg(load); ok {
			r.load = parsed
		}
	}
	if used, total, ok := darwinMemory(); ok {
		r.memUsed, r.memTotal = i64(used), i64(total)
	}
	if tv, err := unix.SysctlTimeval("kern.boottime"); err == nil && tv.Sec > 0 {
		if up := time.Since(time.Unix(tv.Sec, 0)).Seconds(); up > 0 {
			r.uptimeS = i64(int64(up))
		}
	}
	r.diskUsed, r.diskTotal = diskUsage(r.diskPath)
	r.cpu = darwinCPU(ctx)
	return r, nil
}

// darwinMemory reports used and total physical memory.
//
// Total is hw.memsize. Used is what is left once everything the kernel could
// hand to a new allocation is taken out: free and speculative pages, purgeable
// pages, and the external (file-backed) pageable pages that are just cache.
// That mirrors Linux's MemAvailable rather than Activity Monitor's "Memory
// Used", so the two platforms' numbers mean the same thing.
func darwinMemory() (used, total int64, ok bool) {
	memsize, ok := sysctlUint("hw.memsize")
	if !ok || memsize == 0 {
		return 0, 0, false
	}
	pagesize, ok := sysctlUint("hw.pagesize")
	if !ok || pagesize == 0 {
		return 0, 0, false
	}
	var available uint64
	for _, name := range []string{
		"vm.page_free_count",
		"vm.page_speculative_count",
		"vm.page_purgeable_count",
		"vm.page_pageable_external_count",
	} {
		v, ok := sysctlUint(name)
		if !ok {
			return 0, 0, false
		}
		available += v * pagesize
	}
	if available > memsize {
		available = memsize
	}
	return int64(memsize - available), int64(memsize), true
}

// darwinCPU samples every process's cumulative CPU time.
func darwinCPU(ctx context.Context) cpuSample {
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,time=").Output()
	if err != nil {
		return cpuSample{}
	}
	perProc, ok := parsePSTimes(string(out))
	if !ok {
		return cpuSample{}
	}
	var busy float64
	for _, secs := range perProc {
		busy += secs
	}
	cpus := runtime.NumCPU()
	if n, ok := sysctlUint("hw.ncpu"); ok && n > 0 {
		cpus = int(n)
	}
	return cpuSample{ok: true, busy: busy, cpus: cpus, perProc: perProc}
}

// sysctlUint reads an integer sysctl whose width varies by oid: neighbours in
// the same vm namespace are four bytes and eight bytes on the same kernel, and
// unix.SysctlUint32 fails outright on the wide ones.
func sysctlUint(name string) (uint64, bool) {
	raw, err := unix.SysctlRaw(name)
	if err != nil {
		return 0, false
	}
	switch len(raw) {
	case 4:
		return uint64(le32(raw)), true
	case 8:
		return le64(raw), true
	}
	return 0, false
}

// diskUsage measures the filesystem holding path, ignoring the blocks reserved
// for root the way `df` does.
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

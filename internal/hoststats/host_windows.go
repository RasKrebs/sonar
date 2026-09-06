package hoststats

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// kernel32 procedures golang.org/x/sys/windows does not wrap. They are
// resolved lazily, once, from the system directory — never from the working
// directory — so this adds no dependency and no DLL-planting surface.
var (
	modkernel32       = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTime = modkernel32.NewProc("GetSystemTimes")
	procGlobalMemory  = modkernel32.NewProc("GlobalMemoryStatusEx")
	procGetTickCount  = modkernel32.NewProc("GetTickCount64")
)

// memoryStatusEx is MEMORYSTATUSEX. Length must be set before the call.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// readHost reads the machine from kernel32. Windows has no load average — the
// concept does not exist in the NT scheduler — so `load` stays null there
// rather than being faked from the processor queue length.
func readHost(_ context.Context) (reading, error) {
	r := reading{diskPath: systemDrive(), kernel: kernelVersion()}

	r.cpu = windowsCPUSample()
	if used, total, ok := windowsMemory(); ok {
		r.memUsed, r.memTotal = u64(used), u64(total)
	}
	if ms, ok := tickCount64(); ok {
		r.uptimeS = i64(int64(ms / 1000))
	}
	r.diskUsed, r.diskTotal = diskUsage(r.diskPath)
	return r, nil
}

// windowsCPUSample reads GetSystemTimes, the system-wide cumulative counters.
func windowsCPUSample() cpuSample {
	var idle, kernel, user windows.Filetime
	ret, _, _ := procGetSystemTime.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if ret == 0 {
		return cpuSample{}
	}
	busy, total, ok := windowsCPU(
		filetime(idle.HighDateTime, idle.LowDateTime),
		filetime(kernel.HighDateTime, kernel.LowDateTime),
		filetime(user.HighDateTime, user.LowDateTime),
	)
	if !ok {
		return cpuSample{}
	}
	return cpuSample{ok: true, busy: busy, total: total, cpus: runtime.NumCPU()}
}

// windowsMemory reports used and total physical memory. "Available" is the
// kernel's own figure, so used means the same thing it does on the other two
// platforms.
func windowsMemory() (used, total uint64, ok bool) {
	st := memoryStatusEx{}
	st.Length = uint32(unsafe.Sizeof(st))
	ret, _, _ := procGlobalMemory.Call(uintptr(unsafe.Pointer(&st)))
	if ret == 0 || st.TotalPhys == 0 || st.AvailPhys > st.TotalPhys {
		return 0, 0, false
	}
	return st.TotalPhys - st.AvailPhys, st.TotalPhys, true
}

// tickCount64 is milliseconds since boot, sleep included.
func tickCount64() (uint64, bool) {
	ret, _, _ := procGetTickCount.Call()
	if ret == 0 {
		return 0, false
	}
	return uint64(ret), true
}

// kernelVersion is the NT version, "10.0.26100".
func kernelVersion() string {
	v := windows.RtlGetVersion()
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}

// systemDrive is the volume Windows booted from, "C:\".
func systemDrive() string {
	if d := os.Getenv("SystemDrive"); d != "" {
		return d + `\`
	}
	return `C:\`
}

// diskUsage measures the volume holding path. Free is the caller's quota-aware
// figure, matching the Bavail the unix collectors use.
func diskUsage(path string) (used, total *int64) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil
	}
	var freeToCaller, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &totalBytes, &totalFree); err != nil {
		return nil, nil
	}
	if totalBytes == 0 || freeToCaller > totalBytes {
		return nil, nil
	}
	return u64(totalBytes - freeToCaller), u64(totalBytes)
}

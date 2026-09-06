//go:build windows

package ports

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// batchGetCwds returns pid -> working directory by reading each process's PEB.
//
// Windows has no lsof and no /proc: the current directory of another process is
// only readable out of that process's own address space. For each pid the
// sequence is
//
//	OpenProcess(PROCESS_QUERY_INFORMATION|PROCESS_VM_READ)
//	NtQueryInformationProcess(ProcessBasicInformation)  -> PebBaseAddress
//	ReadProcessMemory(peb + ProcessParameters)          -> params address
//	ReadProcessMemory(params + CurrentDirectory)        -> UNICODE_STRING
//	ReadProcessMemory(UNICODE_STRING.Buffer)            -> the UTF-16 path
//
// and every one of those steps is allowed to fail. System processes, services
// running as another user and anything protected deny the handle outright, and
// a process may exit between the scan and this call. Following the rule the
// lsof path already lives by, a pid that refuses is skipped and the batch
// carries on: partial results are results, and a scan that returned nothing
// because one system process said no would be worse than one that named
// nine listeners out of ten.
//
// Limitation: a 32-bit sonar cannot read a 64-bit process. Its
// NtQueryInformationProcess is the WOW64 thunk, which reports the 32-bit view,
// and the 64-bit target's structures live above the 4 GB its pointers can
// address. Those pids are skipped and get "" — the same answer they had before
// this file existed. A 64-bit sonar has no such gap: it reads 64-bit processes
// and WOW64 32-bit ones alike, because a WOW64 process keeps a populated
// 64-bit PEB next to its 32-bit one.
func batchGetCwds(pids []int) map[int]string {
	result := make(map[int]string, len(pids))
	if len(pids) == 0 {
		return result
	}
	layout := nativeLayout()
	wow64 := selfIsWow64()
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if cwd := processCwd(uint32(pid), layout, wow64); cwd != "" {
			result[pid] = cwd
		}
	}
	return result
}

// selfIsWow64 reports whether this sonar is a 32-bit binary running on 64-bit
// Windows. A native 32-bit Windows has no 64-bit processes to fail on, and a
// 64-bit sonar is never WOW64, so this is only ever true in the one build that
// has the limitation documented above.
func selfIsWow64() bool {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return false
	}
	var is bool
	if err := windows.IsWow64Process(windows.CurrentProcess(), &is); err != nil {
		return false
	}
	return is
}

// processCwd resolves one pid, returning "" for every failure.
func processCwd(pid uint32, layout pebLayout, selfWow64 bool) string {
	h, err := openForRead(pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	if selfWow64 && !targetIsWow64(h) {
		// A 32-bit sonar looking at a 64-bit process: unsupported, see above.
		return ""
	}

	var pbi windows.PROCESS_BASIC_INFORMATION
	var retLen uint32
	if err := windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation,
		unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), &retLen); err != nil {
		return ""
	}
	peb := uintptr(unsafe.Pointer(pbi.PebBaseAddress))
	if peb == 0 {
		return ""
	}

	// PEB.ProcessParameters
	buf := make([]byte, layout.ptrSize)
	if !readMemory(h, peb+layout.processParameters, buf) {
		return ""
	}
	params, ok := layout.pointerAt(buf, 0)
	if !ok || params == 0 {
		return ""
	}

	// RTL_USER_PROCESS_PARAMETERS.CurrentDirectory.DosPath
	us := make([]byte, layout.unicodeSize)
	if !readMemory(h, uintptr(params)+layout.currentDirectory, us) {
		return ""
	}
	length, addr, ok := layout.unicodeStringAt(us, 0)
	if !ok {
		return ""
	}

	// UNICODE_STRING.Buffer
	path := make([]byte, length)
	if !readMemory(h, uintptr(addr), path) {
		return ""
	}
	raw := decodeUTF16LE(path)
	if raw == "" {
		return ""
	}
	return normalizeWindowsCwd(longPathName(raw))
}

// openForRead opens a process for the reads above. PROCESS_QUERY_INFORMATION
// is what NtQueryInformationProcess documents; PROCESS_QUERY_LIMITED_INFORMATION
// is enough on Vista and later and is granted in places the full right is not,
// so it is worth a second attempt before giving up on a pid.
func openForRead(pid uint32) (windows.Handle, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err == nil {
		return h, nil
	}
	return windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, pid)
}

// targetIsWow64 reports whether the process behind h is a 32-bit process on
// 64-bit Windows. A handle that cannot answer is treated as 64-bit, i.e. as one
// this build must not try to read.
func targetIsWow64(h windows.Handle) bool {
	var is bool
	if err := windows.IsWow64Process(h, &is); err != nil {
		return false
	}
	return is
}

// readMemory fills buf from the target's address space, insisting on a complete
// read: a partial one means the address was wrong, and half a pointer decodes
// to a plausible-looking lie.
func readMemory(h windows.Handle, addr uintptr, buf []byte) bool {
	if len(buf) == 0 {
		return false
	}
	var read uintptr
	if err := windows.ReadProcessMemory(h, addr, &buf[0], uintptr(len(buf)), &read); err != nil {
		return false
	}
	return int(read) == len(buf)
}

// longPathName expands an 8.3 short name (`C:\PROGRA~1\api`) to its long
// spelling. The PEB stores whatever the process was launched with, while
// groups.Canonical runs filepath.EvalSymlinks, which on Windows always reports
// the long form — so without this the same directory would key the group index
// twice. A path that no longer exists, or one GetLongPathName refuses, is kept
// as it was: a short-name group is still better than no group.
func longPathName(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	n, err := windows.GetLongPathName(p, nil, 0)
	if err != nil || n == 0 {
		return path
	}
	buf := make([]uint16, n)
	n, err = windows.GetLongPathName(p, &buf[0], n)
	if err != nil || n == 0 || int(n) > len(buf) {
		return path
	}
	return windows.UTF16ToString(buf[:n])
}

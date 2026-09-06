package ports

import (
	"strings"
	"unicode/utf16"
	"unsafe"
)

// Windows keeps a process's current directory in its own address space, in the
// PEB, not in any table the kernel will hand out. Reading it is therefore
// pointer arithmetic over three hops of foreign memory:
//
//	PEB.ProcessParameters                     -> RTL_USER_PROCESS_PARAMETERS
//	RTL_USER_PROCESS_PARAMETERS.CurrentDirectory.DosPath -> UNICODE_STRING
//	UNICODE_STRING.Buffer                     -> the UTF-16 path itself
//
// None of that arithmetic needs Windows to be exercised, so the offsets and the
// decoding live here, in a file that builds everywhere and is unit-tested on
// the developer's machine. cwd_windows.go supplies the three reads.

// pebLayout is the byte geometry of those structures for one pointer size.
// Every field is an offset in bytes from the start of its structure.
type pebLayout struct {
	// ptrSize is the width of a pointer in the target process: 8 for a 64-bit
	// process read from a 64-bit sonar, 4 for a 32-bit process read from a
	// 32-bit sonar.
	ptrSize int
	// processParameters is PEB.ProcessParameters.
	processParameters uintptr
	// currentDirectory is RTL_USER_PROCESS_PARAMETERS.CurrentDirectory.DosPath,
	// i.e. the UNICODE_STRING at the head of the CURDIR.
	currentDirectory uintptr
	// unicodeBuffer is UNICODE_STRING.Buffer; unicodeSize is sizeof(UNICODE_STRING).
	unicodeBuffer uintptr
	unicodeSize   int
}

// layout64 is the geometry for 64-bit targets, spelled out so a test can pin it
// without trusting the same arithmetic it is checking. These are the numbers
// every debugger prints for ntdll's structures on amd64/arm64.
var layout64 = pebLayout{
	ptrSize:           8,
	processParameters: 0x20,
	currentDirectory:  0x38,
	unicodeBuffer:     0x08,
	unicodeSize:       0x10,
}

// layout32 is the same geometry for 32-bit targets.
var layout32 = pebLayout{
	ptrSize:           4,
	processParameters: 0x10,
	currentDirectory:  0x24,
	unicodeBuffer:     0x04,
	unicodeSize:       0x08,
}

// The mirror structs below exist only so the layout can be *derived* rather
// than asserted: Go lays them out with the same natural alignment the C
// declarations get, so unsafe.Offsetof reproduces ntdll's offsets for whatever
// word size this binary was built for. They are never allocated.

type mirrorUnicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        uintptr
}

type mirrorCurDir struct {
	DosPath mirrorUnicodeString
	Handle  uintptr
}

type mirrorProcessParameters struct {
	MaximumLength    uint32
	Length           uint32
	Flags            uint32
	DebugFlags       uint32
	ConsoleHandle    uintptr
	ConsoleFlags     uint32
	StandardInput    uintptr
	StandardOutput   uintptr
	StandardError    uintptr
	CurrentDirectory mirrorCurDir
}

type mirrorPEB struct {
	InheritedAddressSpace    byte
	ReadImageFileExecOptions byte
	BeingDebugged            byte
	BitField                 byte
	Mutant                   uintptr
	ImageBaseAddress         uintptr
	Ldr                      uintptr
	ProcessParameters        uintptr
}

// nativeLayout is the geometry of a process with this binary's own word size.
// A 64-bit sonar reads 64-bit PEBs — including those of WOW64 processes, which
// keep a populated 64-bit PEB alongside their 32-bit one.
func nativeLayout() pebLayout {
	return pebLayout{
		ptrSize:           int(unsafe.Sizeof(uintptr(0))),
		processParameters: unsafe.Offsetof(mirrorPEB{}.ProcessParameters),
		currentDirectory:  unsafe.Offsetof(mirrorProcessParameters{}.CurrentDirectory),
		unicodeBuffer:     unsafe.Offsetof(mirrorUnicodeString{}.Buffer),
		unicodeSize:       int(unsafe.Sizeof(mirrorUnicodeString{})),
	}
}

// pointerAt decodes the little-endian pointer stored at off. ok is false when
// buf is too short, which is how a short or refused ReadProcessMemory surfaces.
func (l pebLayout) pointerAt(buf []byte, off uintptr) (addr uint64, ok bool) {
	end := off + uintptr(l.ptrSize)
	if uintptr(len(buf)) < end {
		return 0, false
	}
	for i := l.ptrSize - 1; i >= 0; i-- {
		addr = addr<<8 | uint64(buf[off+uintptr(i)])
	}
	return addr, true
}

// unicodeStringAt decodes the UNICODE_STRING that starts at off: its Length (in
// bytes, not characters — the field is famously not a count of runes) and the
// address of its buffer. ok is false for a truncated read, an odd Length, an
// empty Length or a null buffer. Length needs no upper bound of its own: it is
// a uint16, so the worst a wrong offset can ask us to allocate is 64 KiB.
func (l pebLayout) unicodeStringAt(buf []byte, off uintptr) (length int, addr uint64, ok bool) {
	if uintptr(len(buf)) < off+uintptr(l.unicodeSize) {
		return 0, 0, false
	}
	length = int(buf[off]) | int(buf[off+1])<<8
	addr, ok = l.pointerAt(buf, off+l.unicodeBuffer)
	if !ok || addr == 0 {
		return 0, 0, false
	}
	if length <= 0 || length%2 != 0 {
		return 0, 0, false
	}
	return length, addr, true
}

// decodeUTF16LE turns the raw bytes of a UNICODE_STRING buffer into a Go
// string. A trailing NUL is dropped: the PEB's DosPath is usually NUL-padded
// even though Length excludes the terminator, and a stray one would otherwise
// travel all the way into a group name.
func decodeUTF16LE(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
	}
	for len(units) > 0 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	return string(utf16.Decode(units))
}

// normalizeWindowsCwd turns the PEB's spelling of a directory into the one the
// rest of sonar uses as a map key.
//
// Two things have to go. The PEB always stores a trailing backslash
// (`C:\src\api\`), and groups.Canonical — which is filepath.Clean plus
// EvalSymlinks — never produces one, so an unstripped path is a second entry
// for the same directory and the group silently splits in two. And the
// extended-length `\\?\` prefix, which shows up for processes started with a
// long path, is not a spelling any other tool prints: `\\?\C:\src` and
// `C:\src` are the same directory and must not be two groups. `\\?\UNC\host\share`
// is the same escape for a network path and unfolds back to `\\host\share`.
//
// A bare drive root keeps its backslash: `C:\` is the root of C:, while `C:`
// on its own means "whatever directory that drive is parked at", which is a
// different — and unstable — place.
func normalizeWindowsCwd(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if rest, found := strings.CutPrefix(s, `\\?\`); found {
		if unc, isUNC := strings.CutPrefix(rest, `UNC\`); isUNC {
			s = `\\` + unc
		} else {
			s = rest
		}
	}
	for len(s) > 1 && (s[len(s)-1] == '\\' || s[len(s)-1] == '/') {
		if isDriveRoot(s) {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// isDriveRoot reports whether s is exactly `X:\` or `X:/`.
func isDriveRoot(s string) bool {
	if len(s) != 3 || s[1] != ':' {
		return false
	}
	if s[2] != '\\' && s[2] != '/' {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

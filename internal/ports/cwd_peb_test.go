package ports

import (
	"strings"
	"testing"
	"unsafe"
)

// The PEB walk is pointer arithmetic over another process's memory: get an
// offset wrong and ReadProcessMemory happily returns bytes that decode into a
// plausible path from the wrong field. Nothing here needs Windows — the
// geometry and the decoding are the same numbers on any host — so it is pinned
// twice: against the offsets Go derives from the mirror structs, and against
// the constants a debugger prints for ntdll on a 64-bit machine.

func TestNativeLayoutMatchesTheHardCoded64BitGeometry(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("the 64-bit table only describes a 64-bit host")
	}
	got := nativeLayout()
	if got != layout64 {
		t.Fatalf("nativeLayout() = %+v, want %+v", got, layout64)
	}
	// Spelled out again so a change to layout64 cannot quietly agree with a
	// change to the mirror structs.
	if got.processParameters != 0x20 {
		t.Errorf("PEB.ProcessParameters = %#x, want 0x20", got.processParameters)
	}
	if got.currentDirectory != 0x38 {
		t.Errorf("RTL_USER_PROCESS_PARAMETERS.CurrentDirectory = %#x, want 0x38", got.currentDirectory)
	}
	if got.unicodeBuffer != 0x08 || got.unicodeSize != 0x10 {
		t.Errorf("UNICODE_STRING buffer/size = %#x/%#x, want 0x8/0x10", got.unicodeBuffer, got.unicodeSize)
	}
}

func TestLayout32IsTheHalfWidthGeometry(t *testing.T) {
	want := pebLayout{ptrSize: 4, processParameters: 0x10, currentDirectory: 0x24, unicodeBuffer: 0x04, unicodeSize: 0x08}
	if layout32 != want {
		t.Fatalf("layout32 = %+v, want %+v", layout32, want)
	}
}

func TestPointerAtDecodesLittleEndianAndRefusesShortReads(t *testing.T) {
	buf := []byte{
		0xEF, 0xBE, 0xAD, 0xDE, 0x00, 0x00, 0x00, 0x00, // 0x00000000deadbeef
		0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x00, // 0x0077665544332211
	}
	if got, ok := layout64.pointerAt(buf, 0); !ok || got != 0xdeadbeef {
		t.Errorf("pointerAt(0) = %#x, %v; want 0xdeadbeef, true", got, ok)
	}
	if got, ok := layout64.pointerAt(buf, 8); !ok || got != 0x0077665544332211 {
		t.Errorf("pointerAt(8) = %#x, %v; want 0x77665544332211, true", got, ok)
	}
	if got, ok := layout32.pointerAt(buf, 0); !ok || got != 0xdeadbeef {
		t.Errorf("32-bit pointerAt(0) = %#x, %v; want 0xdeadbeef, true", got, ok)
	}
	// A refused or truncated ReadProcessMemory arrives as a short buffer, and
	// half a pointer must never decode to a usable address.
	if _, ok := layout64.pointerAt(buf[:7], 0); ok {
		t.Error("pointerAt accepted a 7-byte buffer for a 64-bit pointer")
	}
	if _, ok := layout64.pointerAt(buf, 9); ok {
		t.Error("pointerAt read past the end of the buffer")
	}
}

// synthUnicodeString builds the 16 bytes a 64-bit UNICODE_STRING occupies.
func synthUnicodeString(length, maxLength uint16, buffer uint64) []byte {
	b := make([]byte, layout64.unicodeSize)
	b[0], b[1] = byte(length), byte(length>>8)
	b[2], b[3] = byte(maxLength), byte(maxLength>>8)
	for i := 0; i < 8; i++ {
		b[int(layout64.unicodeBuffer)+i] = byte(buffer >> (8 * i))
	}
	return b
}

func TestUnicodeStringAtReadsLengthAndBuffer(t *testing.T) {
	// C:\src\api is 10 characters, so Length is 20 bytes — the field counts
	// bytes, not characters, and reading it as characters would truncate the
	// path in half.
	us := synthUnicodeString(20, 22, 0x00007ffe12340000)
	length, addr, ok := layout64.unicodeStringAt(us, 0)
	if !ok {
		t.Fatal("unicodeStringAt refused a well-formed UNICODE_STRING")
	}
	if length != 20 {
		t.Errorf("length = %d, want 20 bytes", length)
	}
	if addr != 0x00007ffe12340000 {
		t.Errorf("buffer = %#x, want 0x7ffe12340000", addr)
	}
}

func TestUnicodeStringAtHonoursTheCurrentDirectoryOffset(t *testing.T) {
	// The real read lands the whole RTL_USER_PROCESS_PARAMETERS prefix in the
	// buffer, so the UNICODE_STRING is decoded at CurrentDirectory, not at 0.
	params := make([]byte, int(layout64.currentDirectory)+layout64.unicodeSize)
	copy(params[layout64.currentDirectory:], synthUnicodeString(8, 10, 0xCAFE0000))
	length, addr, ok := layout64.unicodeStringAt(params, layout64.currentDirectory)
	if !ok || length != 8 || addr != 0xCAFE0000 {
		t.Fatalf("unicodeStringAt at CurrentDirectory = %d, %#x, %v; want 8, 0xcafe0000, true", length, addr, ok)
	}
	// Decoding at 0 instead reads MaximumLength/Flags as a length and a null
	// pointer as a buffer: exactly the silent-garbage failure the offset
	// prevents.
	if _, _, ok := layout64.unicodeStringAt(params, 0); ok {
		t.Error("a UNICODE_STRING decoded out of the zeroed header was accepted")
	}
}

func TestUnicodeStringAtRejectsImplausibleValues(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
	}{
		{"truncated", synthUnicodeString(20, 22, 0x1000)[:8]},
		{"null buffer", synthUnicodeString(20, 22, 0)},
		{"zero length", synthUnicodeString(0, 22, 0x1000)},
		{"odd length", synthUnicodeString(21, 22, 0x1000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := layout64.unicodeStringAt(tc.buf, 0); ok {
				t.Error("unicodeStringAt accepted it")
			}
		})
	}
}

func TestDecodeUTF16LE(t *testing.T) {
	encode := func(s string, trailingNULs int) []byte {
		var b []byte
		for _, r := range []rune(s) {
			if r > 0xFFFF {
				r -= 0x10000
				hi := 0xD800 + uint16(r>>10)
				lo := 0xDC00 + uint16(r&0x3FF)
				b = append(b, byte(hi), byte(hi>>8), byte(lo), byte(lo>>8))
				continue
			}
			b = append(b, byte(r), byte(r>>8))
		}
		for i := 0; i < trailingNULs; i++ {
			b = append(b, 0, 0)
		}
		return b
	}
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"ascii path", encode(`C:\src\api`, 0), `C:\src\api`},
		{"NUL terminated", encode(`C:\src\api`, 1), `C:\src\api`},
		{"non-ascii", encode(`C:\Users\Rasmus Krebs\Bürö`, 0), `C:\Users\Rasmus Krebs\Bürö`},
		{"surrogate pair", encode("C:\\emoji\\🚀", 0), "C:\\emoji\\🚀"},
		{"empty", nil, ""},
		{"odd trailing byte is ignored", []byte{0x43, 0x00, 0x3A}, "C"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeUTF16LE(tc.in); got != tc.want {
				t.Errorf("decodeUTF16LE = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeWindowsCwd(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The PEB always stores the trailing backslash; groups.Canonical never
		// does, so leaving it on splits one project into two groups.
		{"trailing backslash", `C:\src\api\`, `C:\src\api`},
		{"no trailing backslash", `C:\src\api`, `C:\src\api`},
		{"drive root keeps its backslash", `C:\`, `C:\`},
		{"lowercase drive root", `d:\`, `d:\`},
		{"extended-length prefix", `\\?\C:\src\api\`, `C:\src\api`},
		{"extended-length UNC", `\\?\UNC\build\share\api\`, `\\build\share\api`},
		{"plain UNC", `\\build\share\api\`, `\\build\share\api`},
		{"space padded", "  C:\\src\\api\\  ", `C:\src\api`},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeWindowsCwd(tc.in)
			if got != tc.want {
				t.Errorf("normalizeWindowsCwd(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.HasPrefix(got, `\\?\`) {
				t.Errorf("normalizeWindowsCwd(%q) kept the \\\\?\\ prefix", tc.in)
			}
		})
	}
}

// TestPEBWalkOverASyntheticProcessImage is the whole three-hop walk done over a
// byte slice standing in for another process's address space: it proves the
// offsets compose, not just that each one is right on its own.
func TestPEBWalkOverASyntheticProcessImage(t *testing.T) {
	const (
		pebAddr    = uint64(0x00007FF700000000)
		paramsAddr = uint64(0x00007FF700010000)
		pathAddr   = uint64(0x00007FF700020000)
		base       = pebAddr
	)
	l := layout64
	mem := make([]byte, 0x30000)
	read := func(addr uint64, n int) []byte {
		off := addr - base
		return mem[off : off+uint64(n)]
	}
	writePtr := func(addr, value uint64) {
		b := read(addr, l.ptrSize)
		for i := 0; i < l.ptrSize; i++ {
			b[i] = byte(value >> (8 * i))
		}
	}

	// PEB.ProcessParameters -> params, params.CurrentDirectory -> the string.
	writePtr(pebAddr+uint64(l.processParameters), paramsAddr)
	copy(read(paramsAddr+uint64(l.currentDirectory), l.unicodeSize),
		synthUnicodeString(uint16(len(`C:\src\api`)*2), uint16(len(`C:\src\api`)*2+2), pathAddr))
	pathBytes := read(pathAddr, len(`C:\src\api`)*2)
	for i, r := range []rune(`C:\src\api`) {
		pathBytes[i*2], pathBytes[i*2+1] = byte(r), byte(r>>8)
	}

	params, ok := l.pointerAt(read(pebAddr+uint64(l.processParameters), l.ptrSize), 0)
	if !ok || params != paramsAddr {
		t.Fatalf("ProcessParameters = %#x, %v; want %#x", params, ok, paramsAddr)
	}
	length, addr, ok := l.unicodeStringAt(read(params+uint64(l.currentDirectory), l.unicodeSize), 0)
	if !ok || addr != pathAddr {
		t.Fatalf("CurrentDirectory.DosPath = %d, %#x, %v; want %#x", length, addr, ok, pathAddr)
	}
	if got := normalizeWindowsCwd(decodeUTF16LE(read(addr, length))); got != `C:\src\api` {
		t.Fatalf("the walk produced %q, want %q", got, `C:\src\api`)
	}
}

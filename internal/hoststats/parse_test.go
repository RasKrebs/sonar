package hoststats

import (
	"math"
	"testing"
	"time"
)

// Two consecutive /proc/stat reads from an idle-ish Linux box, one second
// apart. The maths that turns them into a percentage is the whole of the Linux
// CPU collector, so it is pinned here rather than left to a live machine.
const (
	procStatT0 = `cpu  1256789 3421 298765 18904321 45678 0 12345 0 0 0
cpu0 314197 855 74691 4726080 11419 0 3086 0 0 0
cpu1 314197 855 74691 4726080 11419 0 3086 0 0 0
intr 987654321 12 0 0
ctxt 1234567890
btime 1757000000
processes 987654
procs_running 2
procs_blocked 0
`
	procStatT1 = `cpu  1256889 3421 298815 18904821 45678 0 12345 0 0 0
cpu0 314222 855 74703 4726205 11419 0 3086 0 0 0
intr 987654999 12 0 0
`
)

func TestParseProcStatComputesBusyAndTotal(t *testing.T) {
	busy, total, ok := parseProcStat(procStatT0)
	if !ok {
		t.Fatal("parseProcStat did not find the aggregate cpu line")
	}
	// user+nice+system+idle+iowait+irq+softirq+steal
	wantTotal := 1256789.0 + 3421 + 298765 + 18904321 + 45678 + 0 + 12345 + 0
	wantBusy := wantTotal - 18904321 - 45678
	if total != wantTotal || busy != wantBusy {
		t.Errorf("parseProcStat = busy %v total %v, want %v / %v", busy, total, wantBusy, wantTotal)
	}
}

// The percentage is the ratio of the two deltas: 150 busy jiffies out of 650.
func TestProcStatPercentBetweenTwoSamples(t *testing.T) {
	b0, t0, _ := parseProcStat(procStatT0)
	b1, t1, _ := parseProcStat(procStatT1)

	prev := cpuSample{ok: true, busy: b0, total: t0}
	cur := cpuSample{ok: true, busy: b1, total: t1}
	got, ok := percentBetween(prev, clock, cur, clock.Add(time.Second))
	if !ok {
		t.Fatal("percentBetween refused two good samples")
	}
	want := math.Round((150.0/650.0*100)*10) / 10 // 23.1
	if got != want {
		t.Errorf("cpu percent = %v, want %v", got, want)
	}
}

func TestParseProcStatRejectsRubbish(t *testing.T) {
	for _, in := range []string{"", "intr 1 2 3\n", "cpu  x y z w\n", "cpu  1 2\n"} {
		if _, _, ok := parseProcStat(in); ok {
			t.Errorf("parseProcStat(%q) accepted a line it cannot use", in)
		}
	}
}

func TestParseLoadavg(t *testing.T) {
	load, ok := parseLoadavg("0.42 0.51 0.60 1/523 12345\n")
	if !ok {
		t.Fatal("parseLoadavg refused a good /proc/loadavg")
	}
	want := []float64{0.42, 0.51, 0.60}
	for i := range want {
		if load[i] != want[i] {
			t.Errorf("load = %v, want %v", load, want)
			break
		}
	}
	if _, ok := parseLoadavg("0.42 0.51\n"); ok {
		t.Error("parseLoadavg accepted a truncated line")
	}
}

func TestParseUptime(t *testing.T) {
	got, ok := parseUptime("93607.42 712345.11\n")
	if !ok || got != 93607 {
		t.Errorf("parseUptime = %d, %v, want 93607, true", got, ok)
	}
	if _, ok := parseUptime("\n"); ok {
		t.Error("parseUptime accepted an empty file")
	}
}

const meminfo = `MemTotal:       32762136 kB
MemFree:         1029412 kB
MemAvailable:   23068672 kB
Buffers:          512000 kB
Cached:         18874368 kB
SwapCached:            0 kB
Active:         12345678 kB
`

func TestParseMeminfoUsesMemAvailable(t *testing.T) {
	used, total, ok := parseMeminfo(meminfo)
	if !ok {
		t.Fatal("parseMeminfo refused a good /proc/meminfo")
	}
	wantTotal := int64(32762136) * 1024
	wantUsed := wantTotal - int64(23068672)*1024
	if total != wantTotal || used != wantUsed {
		t.Errorf("parseMeminfo = used %d total %d, want %d / %d", used, total, wantUsed, wantTotal)
	}
}

// Kernels before 3.14 have no MemAvailable; free + buffers + cached stands in.
func TestParseMeminfoFallsBackWithoutMemAvailable(t *testing.T) {
	old := `MemTotal:       32762136 kB
MemFree:         1029412 kB
Buffers:          512000 kB
Cached:         18874368 kB
`
	used, total, ok := parseMeminfo(old)
	if !ok {
		t.Fatal("parseMeminfo refused a pre-3.14 /proc/meminfo")
	}
	wantUsed := (int64(32762136) - 1029412 - 512000 - 18874368) * 1024
	if used != wantUsed || total != int64(32762136)*1024 {
		t.Errorf("parseMeminfo = used %d total %d, want used %d", used, total, wantUsed)
	}
}

func TestParseMeminfoWithoutMemTotalFails(t *testing.T) {
	if _, _, ok := parseMeminfo("MemFree: 100 kB\n"); ok {
		t.Error("parseMeminfo accepted a file with no MemTotal")
	}
}

// A captured `ps -A -o pid=,time=` from macOS: kernel_task, a couple of user
// processes, and one that has been running for days.
const psSample = `    0  40:18.12
    1   0:01.42
  501  24:55.63
  623 2-03:14:15
  bad  0:01.00
`

func TestParsePSTimes(t *testing.T) {
	got, ok := parsePSTimes(psSample)
	if !ok {
		t.Fatal("parsePSTimes refused a good ps table")
	}
	if len(got) != 4 {
		t.Fatalf("parsePSTimes returned %d rows, want 4 (the unparseable pid is dropped)", len(got))
	}
	if want := 40*60 + 18.12; got[0] != want {
		t.Errorf("pid 0 = %v, want %v", got[0], want)
	}
	if want := 2*86400 + 3*3600 + 14*60 + 15.0; got[623] != want {
		t.Errorf("pid 623 = %v, want %v", got[623], want)
	}
	if _, ok := parsePSTimes("no rows here\n"); ok {
		t.Error("parsePSTimes accepted output with no process in it")
	}
}

func TestParseCPUTimeRejectsNonsense(t *testing.T) {
	for _, in := range []string{"", "12", "1:2:3:4", "a:b", "-1:00"} {
		if _, ok := parseCPUTime(in); ok {
			t.Errorf("parseCPUTime(%q) accepted a value it cannot read", in)
		}
	}
}

// A process that exits between two samples takes its lifetime's CPU with it;
// one that starts brings its whole (short) lifetime. Neither may push the
// interval's total negative.
func TestPerProcDeltaHandlesChurn(t *testing.T) {
	prev := map[int]float64{1: 100, 2: 5000, 3: 10}
	cur := map[int]float64{1: 101.5, 3: 10, 4: 0.25} // pid 2 exited, pid 4 is new
	if got, want := perProcDelta(prev, cur), 1.75; math.Abs(got-want) > 1e-9 {
		t.Errorf("perProcDelta = %v, want %v", got, want)
	}
}

// darwin's `vm.loadavg` is a struct loadavg: three fixed-point values and the
// scale to divide them by. This is the 24 bytes an arm64 kernel returns for a
// load of 1.5 / 0.75 / 0.25 at the classic FSCALE of 2048.
func TestParseSysctlLoadavg(t *testing.T) {
	raw := make([]byte, 24)
	put32(raw[0:], 3072)  // 1.5
	put32(raw[4:], 1536)  // 0.75
	put32(raw[8:], 512)   // 0.25
	put32(raw[16:], 2048) // fscale, first word of the 64-bit long
	got, ok := parseSysctlLoadavg(raw)
	if !ok {
		t.Fatal("parseSysctlLoadavg refused a well-formed struct")
	}
	want := []float64{1.5, 0.75, 0.25}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("load = %v, want %v", got, want)
		}
	}
	if _, ok := parseSysctlLoadavg(raw[:8]); ok {
		t.Error("parseSysctlLoadavg accepted a truncated struct")
	}
}

func put32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// GetSystemTimes hands back three FILETIMEs, split into two 32-bit halves, and
// counts idle time inside the kernel total. This is the decoding a Windows
// daemon does every tick, checked on whatever OS the tests run on.
func TestWindowsCPUFromFiletimes(t *testing.T) {
	// idle 40 s, kernel 60 s (idle included), user 20 s, in 100 ns units.
	const unit = 10_000_000
	idle := uint64(40 * unit)
	kernel := uint64(60 * unit)
	user := uint64(20 * unit)

	if got := filetime(uint32(idle>>32), uint32(idle)); got != idle {
		t.Fatalf("filetime round trip = %d, want %d", got, idle)
	}

	busy, total, ok := windowsCPU(idle, kernel, user)
	if !ok {
		t.Fatal("windowsCPU refused a good sample")
	}
	if total != float64(80*unit) || busy != float64(40*unit) {
		t.Errorf("windowsCPU = busy %v total %v, want %v / %v",
			busy, total, float64(40*unit), float64(80*unit))
	}

	// The same counters one second later, with half a second of work done.
	prev := cpuSample{ok: true, busy: busy, total: total}
	cur := cpuSample{ok: true, busy: busy + 0.5*unit, total: total + 1*unit}
	pct, ok := percentBetween(prev, clock, cur, clock.Add(time.Second))
	if !ok || pct != 50 {
		t.Errorf("windows cpu percent = %v, %v, want 50", pct, ok)
	}
}

func TestWindowsCPURejectsImpossibleCounters(t *testing.T) {
	if _, _, ok := windowsCPU(100, 0, 0); ok {
		t.Error("windowsCPU accepted an idle time with no total")
	}
	if _, _, ok := windowsCPU(200, 100, 50); ok {
		t.Error("windowsCPU accepted more idle than total")
	}
}

package hoststats

import (
	"context"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/state"
)

// clock is the instant the fake-clock tests start from.
var clock = time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)

// fakeCollector builds a Collector over a scripted clock and a scripted
// platform reading, so the CPU maths is exercised without an OS.
func fakeCollector(readings ...reading) *Collector {
	now := clock
	i := 0
	return &Collector{
		now: func() time.Time {
			// Every collection advances the clock by one scan interval.
			at := now
			now = now.Add(2 * time.Second)
			return at
		},
		read: func(context.Context) (reading, error) {
			r := readings[i]
			if i < len(readings)-1 {
				i++
			}
			return r, nil
		},
	}
}

func linuxReading(busy, total float64) reading {
	return reading{
		kernel:  "6.8.0-40-generic",
		uptimeS: i64(93607),
		load:    []float64{0.42, 0.51, 0.6},
		cpu:     cpuSample{ok: true, busy: busy, total: total},
	}
}

// The first tick has nothing to compare against, so cpu_percent is null rather
// than a made-up zero.
func TestFirstCollectHasNoCPUPercent(t *testing.T) {
	c := fakeCollector(linuxReading(100, 1000))
	h, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if h.CPUPercent != nil {
		t.Errorf("cpu_percent = %v on the first tick, want null", *h.CPUPercent)
	}
	if h.Name != state.LocalhostName || h.Address != state.LocalhostName {
		t.Errorf("host identity = %q/%q, want localhost", h.Name, h.Address)
	}
	if h.Status != state.HostConnected {
		t.Errorf("status = %q, want connected", h.Status)
	}
	if h.Kernel != "6.8.0-40-generic" || h.UptimeS == nil || *h.UptimeS != 93607 {
		t.Errorf("platform fields did not survive Collect: %+v", h)
	}
	if h.LastSeen != clock.Format(time.RFC3339) {
		t.Errorf("last_seen = %q, want the collection time", h.LastSeen)
	}
}

// The second tick measures the interval between the two samples.
func TestCPUPercentIsTheDeltaBetweenTwoSamples(t *testing.T) {
	c := fakeCollector(linuxReading(100, 1000), linuxReading(190, 1300))
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	h, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if h.CPUPercent == nil {
		t.Fatal("cpu_percent is null on the second tick")
	}
	if want := 30.0; *h.CPUPercent != want { // 90 busy of 300 total
		t.Errorf("cpu_percent = %v, want %v", *h.CPUPercent, want)
	}
}

// A counter that went backwards — a reboot, a wrapped register — is not a
// negative percentage. The tick reports null and the next one is correct
// again, because the sample was still stored.
func TestCountersGoingBackwardsPublishNull(t *testing.T) {
	c := fakeCollector(linuxReading(500, 1000), linuxReading(100, 1200), linuxReading(160, 1400))
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	h, _ := c.Collect(context.Background())
	if h.CPUPercent != nil {
		t.Errorf("cpu_percent = %v after the counter reset, want null", *h.CPUPercent)
	}
	h, _ = c.Collect(context.Background())
	if h.CPUPercent == nil || *h.CPUPercent != 30 {
		t.Errorf("cpu_percent = %v after recovery, want 30", h.CPUPercent)
	}
}

// macOS has no system-wide counter: the denominator is wall time times cores.
// Two seconds of elapsed time on four cores is eight core-seconds, and two of
// them spent working is 25%.
func TestCPUPercentWithoutASystemTotalUsesWallTimeAndCores(t *testing.T) {
	sample := func(secs float64) reading {
		return reading{cpu: cpuSample{
			ok: true, busy: secs, cpus: 4,
			perProc: map[int]float64{1: secs},
		}}
	}
	c := fakeCollector(sample(10), sample(12))
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	h, _ := c.Collect(context.Background())
	if h.CPUPercent == nil || *h.CPUPercent != 25 {
		t.Errorf("cpu_percent = %v, want 25", h.CPUPercent)
	}
}

// A platform that cannot read its CPU at all publishes null for it and keeps
// everything else.
func TestUnreadableCPUIsNull(t *testing.T) {
	c := fakeCollector(reading{kernel: "unknown"}, reading{kernel: "unknown"})
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	h, _ := c.Collect(context.Background())
	if h.CPUPercent != nil {
		t.Errorf("cpu_percent = %v with no CPU counter, want null", *h.CPUPercent)
	}
}

func TestPercentIsClampedToOneDecimal(t *testing.T) {
	prev := cpuSample{ok: true, busy: 0, total: 0, cpus: 1}
	cur := cpuSample{ok: true, busy: 0.123456, cpus: 1}
	got, ok := percentBetween(prev, clock, cur, clock.Add(time.Second))
	if !ok || got != 12.3 {
		t.Errorf("percentBetween = %v, %v, want 12.3", got, ok)
	}
}

// The real collector on this machine must answer, whatever the OS is, and must
// answer twice with a percentage the second time.
func TestLiveCollectorFillsTheRowTwice(t *testing.T) {
	c := New()
	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	h, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if h.OS == "" || h.Arch == "" {
		t.Errorf("os/arch empty: %+v", h)
	}
	if h.MemoryTotal == nil || *h.MemoryTotal <= 0 {
		t.Errorf("memory_total_bytes = %v, want the machine's RAM", h.MemoryTotal)
	}
	if h.DiskTotal == nil || *h.DiskTotal <= 0 {
		t.Errorf("disk_total_bytes = %v, want the size of %q", h.DiskTotal, h.DiskPath)
	}
	if h.UptimeS == nil || *h.UptimeS <= 0 {
		t.Errorf("uptime_s = %v, want seconds since boot", h.UptimeS)
	}
	if h.CPUPercent == nil {
		t.Error("cpu_percent is null on the second live collection")
	}
}

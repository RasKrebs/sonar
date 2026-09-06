package ports

import (
	"os"
	"runtime"
	"testing"
)

// TestParseProcStatPPID covers the Linux shape ParentTable reads: field 2 of
// /proc/<pid>/stat is the executable name in parentheses, and it may contain
// both spaces and parentheses of its own, so the ppid can only be found from
// the last ')' onwards.
func TestParseProcStatPPID(t *testing.T) {
	cases := []struct {
		name string
		stat string
		want int
		ok   bool
	}{
		{
			name: "a plain process",
			stat: "4242 (listener) S 4200 4200 4200 0 -1 4194304 285 0 0 0 0 0 0 0 20 0 5 0 91234 0 0\n",
			want: 4200,
			ok:   true,
		},
		{
			name: "a comm with spaces and parentheses",
			// Next.js sets process.title to "next-server (v15.0.1)"; the
			// kernel truncates comm to 15 bytes but keeps what fits verbatim.
			stat: "812 (next-server (v) S 799 799 640 0 -1 4194560 9021 0 3 0 41 12 0 0 20 0 11 0 5512",
			want: 799,
			ok:   true,
		},
		{
			name: "a comm that itself looks like a stat line",
			stat: "77 (evil) 1 (x) R 5 5 5 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 42",
			want: 5,
			ok:   true,
		},
		{
			name: "init, whose parent is the kernel",
			stat: "1 (systemd) S 0 1 1 0 -1 4194560 30112 0 0 0 90 250 0 0 20 0 1 0 12",
			want: 0,
			ok:   true,
		},
		{
			name: "a truncated read",
			stat: "4242 (listener) S",
			ok:   false,
		},
		{
			name: "no comm at all",
			stat: "not a stat line",
			ok:   false,
		},
		{
			name: "a non-numeric ppid",
			stat: "4242 (listener) S parent 4200 4200",
			ok:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseProcStatPPID(tc.stat)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("ppid = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestParentTableFindsThisProcess is the end-to-end check that whichever
// implementation the platform uses reports a table containing this test and
// the parent it actually has.
func TestParentTableFindsThisProcess(t *testing.T) {
	table := ParentTable()
	if len(table) == 0 {
		t.Skip("no process table on this machine")
	}
	ppid, ok := table[os.Getpid()]
	if !ok {
		t.Fatalf("the table has no entry for this process (pid %d)", os.Getpid())
	}
	if ppid != os.Getppid() {
		t.Errorf("ppid = %d, want %d", ppid, os.Getppid())
	}
	if runtime.GOOS == "linux" {
		// /proc answered, so `ps` was never needed.
		if native := nativeParentTable(); len(native) == 0 {
			t.Error("/proc yielded no parent table on Linux")
		}
	}
}

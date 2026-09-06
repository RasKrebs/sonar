package ports

import "testing"

// Windows has no /proc and no `ps -A`, so ParentTable used to come back empty
// there and every ancestry walk with it. The parents now ride along on the CIM
// query the scan was making anyway. None of that needs Windows to test: the CSV
// parsing and the table it feeds are ordinary Go.

func TestParseCIMProcesses(t *testing.T) {
	records := [][]string{
		{"ProcessId", "ParentProcessId", "StartedAt", "CommandLine"},
		{"1234", "1000", "2026-09-06T10:00:00.0000000+02:00", `"C:\src\api\node.exe" server.js`},
		// A protected process: Windows withholds the command line but still
		// names the parent, and the row has to survive for the walk's sake.
		{"1235", "1000", "2026-09-06T10:00:01.0000000+02:00", ""},
		// Neither a command line nor a parent is nothing worth keeping.
		{"1236", "0", "", ""},
		// Garbage in the pid column is skipped, not fatal.
		{"nope", "1000", "", "x"},
		// A short row (an older schema, a truncated read) is skipped.
		{"1237", "1000"},
	}
	got := parseCIMProcesses(records)

	if len(got) != 2 {
		t.Fatalf("parseCIMProcesses returned %d rows: %+v", len(got), got)
	}
	if got[1234].command != `"C:\src\api\node.exe" server.js` {
		t.Errorf("command = %q", got[1234].command)
	}
	if got[1234].ppid != 1000 {
		t.Errorf("ppid = %d, want 1000", got[1234].ppid)
	}
	if got[1234].startedAt == "" {
		t.Error("startedAt was dropped")
	}
	if _, ok := got[1235]; !ok {
		t.Error("a row with a parent but no command line was dropped")
	}
	if got[1235].ppid != 1000 {
		t.Errorf("ppid for the command-less row = %d, want 1000", got[1235].ppid)
	}
	if _, ok := got[1236]; ok {
		t.Error("a row with neither a command line nor a parent was kept")
	}
}

func TestRememberScanParentsFeedsTheTable(t *testing.T) {
	restore := scanParentTable.Load()
	t.Cleanup(func() { scanParentTable.Store(restore) })
	scanParentTable.Store(nil)

	if got := scanParents(); len(got) != 0 {
		t.Fatalf("scanParents before any scan = %v, want empty", got)
	}

	rememberScanParents(map[int]procInfo{
		1234: {command: "node", ppid: 1000},
		1235: {command: "python", ppid: 1000},
		1236: {command: "orphan"}, // no parent reported: nothing to record
	})
	got := scanParents()
	if len(got) != 2 || got[1234] != 1000 || got[1235] != 1000 {
		t.Fatalf("scanParents = %v, want {1234:1000, 1235:1000}", got)
	}

	// The caller gets a copy: mutating it must not poison the next reader.
	got[1234] = 9999
	if again := scanParents(); again[1234] != 1000 {
		t.Errorf("the table was aliased: %v", again)
	}

	// A scan that learned nothing leaves the last good table in place rather
	// than blanking it — an empty answer is worse than a slightly stale one.
	rememberScanParents(map[int]procInfo{})
	if again := scanParents(); len(again) != 2 {
		t.Errorf("an empty scan cleared the table: %v", again)
	}
}

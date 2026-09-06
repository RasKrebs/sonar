package ports

import (
	"strings"
	"testing"
)

// Windows has no /proc and no `ps -A`, so ParentTable used to come back empty
// there and every ancestry walk with it. The parents now ride along on the CIM
// query the scan was making anyway. None of that needs Windows to test: the CSV
// parsing and the table it feeds are ordinary Go.

// cimSample is what `Get-CimInstance Win32_Process … | Select-Object
// ProcessId,ParentProcessId,@{N='StartedAt';…},CommandLine | ConvertTo-Csv
// -NoTypeInformation` prints on windows-latest, with the rows that matter:
// a normal process, a protected one whose CommandLine comes back empty, and a
// command line carrying the quotes, spaces and commas CSV has to survive.
const cimSample = "\"ProcessId\",\"ParentProcessId\",\"StartedAt\",\"CommandLine\"\r\n" +
	"\"1234\",\"1000\",\"2026-09-06T10:00:00.0000000+02:00\",\"\"\"C:\\Users\\runneradmin\\AppData\\Local\\Temp\\sonar-itest-listener1\\listener.exe\"\" 59028\"\r\n" +
	"\"1235\",\"1000\",\"2026-09-06T10:00:01.0000000+02:00\",\"\"\r\n" +
	"\"640\",\"512\",\"2026-09-06T09:59:00.0000000+02:00\",\"C:\\Windows\\system32\\svchost.exe -k RPCSS -p, -s RpcEptMapper\"\r\n"

func TestParseCIMProcessesReadsACapturedSample(t *testing.T) {
	got := parseCIMProcesses(cimSample)

	if len(got) != 3 {
		t.Fatalf("parseCIMProcesses returned %d rows: %+v", len(got), got)
	}
	want := `"C:\Users\runneradmin\AppData\Local\Temp\sonar-itest-listener1\listener.exe" 59028`
	if got[1234].command != want {
		t.Errorf("command = %q, want %q", got[1234].command, want)
	}
	if got[1234].ppid != 1000 {
		t.Errorf("ppid = %d, want 1000", got[1234].ppid)
	}
	if got[1234].startedAt == "" {
		t.Error("startedAt was dropped")
	}
	// A protected process withholds its command line and still reports a
	// parent. Dropping the row would cost the ancestry walk that parent.
	if row, ok := got[1235]; !ok || row.ppid != 1000 || row.command != "" {
		t.Errorf("the command-less row = %+v, ok=%v; want a kept row with ppid 1000", row, ok)
	}
	// Commas inside a quoted command line are not column separators.
	if !strings.Contains(got[640].command, "-k RPCSS -p, -s RpcEptMapper") {
		t.Errorf("svchost command = %q, want the commas preserved", got[640].command)
	}
}

// TestParseCIMProcessesFindsColumnsByName: the parser must not depend on the
// order Select-Object happens to emit. A positional parser read a parent pid as
// a start time the moment the query grew a column.
func TestParseCIMProcessesFindsColumnsByName(t *testing.T) {
	reordered := "\"CommandLine\",\"StartedAt\",\"ProcessId\",\"ParentProcessId\"\r\n" +
		"\"node server.js\",\"2026-09-06T10:00:00.0000000+02:00\",\"1234\",\"1000\"\r\n"
	got := parseCIMProcesses(reordered)
	if len(got) != 1 {
		t.Fatalf("parseCIMProcesses returned %d rows: %+v", len(got), got)
	}
	if got[1234].command != "node server.js" || got[1234].ppid != 1000 {
		t.Errorf("row = %+v, want command %q and ppid 1000", got[1234], "node server.js")
	}
}

// TestParseCIMProcessesSurvivesOneBadRow: one process with an unreadable
// command line must not cost every other process on the machine its identity —
// which is what ReadAll's all-or-nothing error did.
func TestParseCIMProcessesSurvivesOneBadRow(t *testing.T) {
	withGarbage := "\"ProcessId\",\"ParentProcessId\",\"StartedAt\",\"CommandLine\"\r\n" +
		"\"1234\",\"1000\",\"\",\"node server.js\"\r\n" +
		"\"nope\",\"x\",\"\",\"skipped: unparsable pid\"\r\n" +
		"\"1236\",\"0\",\"\",\"\"\r\n" +
		"\"1237\",\"1000\",\"\",\"python -m http.server\"\r\n"
	got := parseCIMProcesses(withGarbage)
	if _, ok := got[1234]; !ok {
		t.Error("the row before the bad one was lost")
	}
	if _, ok := got[1237]; !ok {
		t.Error("the row after the bad one was lost")
	}
	if _, ok := got[1236]; ok {
		t.Error("a row with neither a command line nor a parent was kept")
	}
}

func TestParseCIMProcessesHandlesNoOutput(t *testing.T) {
	for _, in := range []string{"", "   \r\n", "\"ProcessId\",\"ParentProcessId\",\"StartedAt\",\"CommandLine\"\r\n"} {
		if got := parseCIMProcesses(in); len(got) != 0 {
			t.Errorf("parseCIMProcesses(%q) = %v, want empty", in, got)
		}
	}
	// A header the parser does not recognise is not a licence to guess.
	if got := parseCIMProcesses("\"Name\",\"Handle\"\r\n\"node\",\"1234\"\r\n"); len(got) != 0 {
		t.Errorf("an unknown header produced %v, want empty", got)
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

//go:build !windows

package sessions

import "testing"

func TestParsePSReadsPidPpidComm(t *testing.T) {
	got := parsePS("  100     1 /usr/bin/claude code\n 200   100 zsh\nnonsense\n")
	if len(got) != 2 {
		t.Fatalf("parsePS returned %d rows: %+v", len(got), got)
	}
	if got[0] != (Process{PID: 100, PPID: 1, Command: "/usr/bin/claude code"}) {
		t.Errorf("first row = %+v", got[0])
	}
}

// The real process table has this process in it, with a plausible parent.
func TestProcessTableIncludesThisProcess(t *testing.T) {
	procs := processTable()
	if len(procs) == 0 {
		t.Skip("no ps on this machine")
	}
	for _, p := range procs {
		if p.Command == "" {
			t.Errorf("process %d has no command", p.PID)
			break
		}
	}
}

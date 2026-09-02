package ports

import "testing"

func TestKillPID_RejectsInvalidPID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if err := KillPID(pid, true); err == nil {
			t.Errorf("KillPID(%d) succeeded, want error", pid)
		}
	}
}

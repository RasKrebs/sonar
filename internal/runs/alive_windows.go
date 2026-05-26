//go:build windows

package runs

import "os/exec"

// pidAlive reports whether a process with the given PID currently exists.
// Best-effort on Windows: query tasklist for the PID and check for output.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+itoa(pid), "/NH").Output()
	if err != nil {
		// If we can't tell, assume alive so we don't prematurely prune.
		return true
	}
	return len(out) > 0 && !containsNoTasks(string(out))
}

func containsNoTasks(s string) bool {
	// tasklist prints "INFO: No tasks are running..." when the PID is gone.
	for i := 0; i+4 < len(s); i++ {
		if s[i] == 'N' && s[i+1] == 'o' && s[i+2] == ' ' && s[i+3] == 't' {
			return true
		}
	}
	return false
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

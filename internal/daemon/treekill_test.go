//go:build integration

package daemon_test

import (
	"os/exec"

	"github.com/raskrebs/sonar/internal/ports"
)

// stopCommand takes a `sonar` invocation down for good and reaps it.
//
// Killing only the process the test started is not enough: `sonar start` puts
// the command it supervises in a process group of its own (daemon spec), so
// the child survives its parent and keeps holding the stdout pipe it
// inherited. Wait then blocks forever copying from a pipe nobody will close,
// which is how a failed assertion turned into a ten-minute CI timeout. The
// whole tree goes, and env.command's WaitDelay is the backstop for anything
// that still escapes.
func stopCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	killTree(cmd)
	_ = cmd.Wait()
}

// descendants lists every process below pid in the process table, deepest
// first, so children are signalled before the parent that could respawn them.
func descendants(pid int) []int {
	children := map[int][]int{}
	for child, parent := range ports.ParentTable() {
		if child != parent && child > 1 {
			children[parent] = append(children[parent], child)
		}
	}
	var out []int
	var walk func(int, int)
	walk = func(p, depth int) {
		if depth > 32 {
			return
		}
		for _, c := range children[p] {
			walk(c, depth+1)
			out = append(out, c)
		}
	}
	walk(pid, 0)
	return out
}

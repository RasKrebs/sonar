//go:build integration && windows

package daemon_test

import "os/exec"

// ownProcessGroup is a no-op on Windows: spawn already puts a started command
// in a Job Object, which is what makes the tree die with it.
func ownProcessGroup(*exec.Cmd) {}

// killTree kills the command, and the Job Object takes the rest of the tree
// with it.
func killTree(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

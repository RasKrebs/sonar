package daemon

import "testing"

func TestIsTestBinary(t *testing.T) {
	tests := map[string]bool{
		"/tmp/go-build123/b001/cmd.test":     true,
		"/tmp/go-build123/b001/daemon.test":  true,
		`C:\Temp\go-build\b001\cmd.test.exe`: true,
		"cmd.test":                           true,
		"/usr/local/bin/sonar":               false,
		`C:\Program Files\sonar\sonar.exe`:   false,
		"/tmp/sonar-itest-bin123/sonar":      false,
		"/home/dev/testing/sonar":            false,
		"/home/dev/sonar.test.backup/sonar":  false,
	}
	for path, want := range tests {
		if got := IsTestBinary(path); got != want {
			t.Errorf("IsTestBinary(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestEnvEnabled(t *testing.T) {
	off := []string{"", "0", "false", "no", " FALSE "}
	for _, v := range off {
		if EnvEnabled(v) {
			t.Errorf("EnvEnabled(%q) = true, want false", v)
		}
	}
	for _, v := range []string{"1", "true", "yes", "anything"} {
		if !EnvEnabled(v) {
			t.Errorf("EnvEnabled(%q) = false, want true", v)
		}
	}
}

func TestTestDaemonAllowedFollowsTheEnvironment(t *testing.T) {
	if TestDaemonAllowed() {
		t.Fatalf("%s is set in a test run; the guard is disarmed", AllowTestDaemonEnv)
	}
	t.Setenv(AllowTestDaemonEnv, "1")
	if !TestDaemonAllowed() {
		t.Error("the explicit opt-in was not honoured")
	}
}

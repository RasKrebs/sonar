package ports

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestParseStartTime(t *testing.T) {
	if got := parseStartTime("Wed Mar 18 10:30:00 2026"); got != "2026-03-18T10:30:00Z" {
		// ps prints local time; compare only the date/time prefix.
		if len(got) < 19 || got[:19] != "2026-03-18T10:30:00" {
			t.Fatalf("parseStartTime(lstart) = %q", got)
		}
	}
	if got := parseStartTime("2026-09-02T09:12:41+02:00"); got != "2026-09-02T09:12:41+02:00" {
		t.Fatalf("parseStartTime(rfc3339) = %q", got)
	}
	if got := parseStartTime("nonsense"); got != "" {
		t.Fatalf("parseStartTime(garbage) = %q", got)
	}
}

// TestParsePSCommands covers the always-on ps table that carries both the
// command line and the process start time. The command may contain any
// whitespace, so it is taken as the remainder of the line, not as fields.
func TestParsePSCommands(t *testing.T) {
	out := `  1234 Wed Mar 18 10:30:00 2026 /usr/bin/node   server.js --port 3000
 567 Wed Mar 18 09:00:01 2026 /Applications/Some App.app/Contents/MacOS/Some App
 bad line
 99 Wed Mar 18 09:00:01 2026
`
	got := parsePSCommands(out)
	if len(got) != 2 {
		t.Fatalf("parsePSCommands returned %d rows, want 2: %+v", len(got), got)
	}
	if want := "/usr/bin/node   server.js --port 3000"; got[1234].command != want {
		t.Errorf("command = %q, want %q", got[1234].command, want)
	}
	if got[1234].startedAt == "" || got[1234].startedAt[:19] != "2026-03-18T10:30:00" {
		t.Errorf("startedAt = %q, want a 2026-03-18T10:30:00 timestamp", got[1234].startedAt)
	}
	if want := "/Applications/Some App.app/Contents/MacOS/Some App"; got[567].command != want {
		t.Errorf("command = %q, want %q", got[567].command, want)
	}
}

// TestEnrichPopulatesStartedAtWithoutStats is the smoke-test regression:
// started_at was null in every state.snapshot row because only EnrichStats
// filled it. Enrich alone must set it, for the process running this test.
func TestEnrichPopulatesStartedAtWithoutStats(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the ps table this exercises is the Unix path")
	}
	pp := []ListeningPort{{Port: 1, PID: os.Getpid(), Process: "sonar.test"}}
	Enrich(pp) // no EnrichStats: started_at is not gated by --stats

	if pp[0].StartedAt == "" {
		t.Fatal("Enrich left started_at empty for the current process")
	}
	started, err := time.Parse(time.RFC3339, pp[0].StartedAt)
	if err != nil {
		t.Fatalf("started_at %q is not RFC3339: %v", pp[0].StartedAt, err)
	}
	if age := time.Since(started); age < 0 || age > 24*time.Hour {
		t.Errorf("started_at %s is %s away from now; want a plausible start time", pp[0].StartedAt, age)
	}
	if pp[0].Uptime != "" || pp[0].CPUPercent != 0 {
		t.Errorf("Enrich collected stats it was not asked for: uptime=%q cpu=%v", pp[0].Uptime, pp[0].CPUPercent)
	}
}

// TestUptimeIsLocalTimeNotUTC pins the fix for a negative `stats.uptime`: `ps`
// prints lstart in local time with no zone, and parsing it as UTC put the start
// instant a whole UTC offset away — so a process that had been up for fifteen
// seconds in a +02:00 zone reported "-7185s" while `started_at`, which already
// parsed in the local zone, was right.
func TestUptimeIsLocalTimeNotUTC(t *testing.T) {
	for _, zone := range []string{"Europe/Copenhagen", "America/Los_Angeles", "UTC"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("no zone database: %v", err)
		}
		t.Run(zone, func(t *testing.T) {
			prev := time.Local
			time.Local = loc
			t.Cleanup(func() { time.Local = prev })

			started := time.Date(2026, 9, 6, 10, 30, 0, 0, loc)
			lstart := started.Format("Mon Jan  2 15:04:05 2006")

			if got := computeUptimeAt(lstart, started.Add(15*time.Second)); got != "15s" {
				t.Errorf("uptime 15s after the start = %q, want 15s", got)
			}
			if got := computeUptimeAt(lstart, started.Add(90*time.Minute)); got != "1h30m" {
				t.Errorf("uptime 90m after the start = %q, want 1h30m", got)
			}
			// started_at and uptime read the same instant, so they agree.
			if got := parseStartTime(lstart); got != started.Format(time.RFC3339) {
				t.Errorf("started_at = %q, want %q", got, started.Format(time.RFC3339))
			}
		})
	}
}

// TestUptimeNeverGoesNegative: a clock that steps back, or a start time a
// moment in the future, reads as 0s rather than as a negative duration.
func TestUptimeNeverGoesNegative(t *testing.T) {
	started := time.Date(2026, 9, 6, 10, 30, 0, 0, time.Local)
	lstart := started.Format("Mon Jan  2 15:04:05 2006")
	if got := computeUptimeAt(lstart, started.Add(-2*time.Hour)); got != "0s" {
		t.Fatalf("uptime with a backwards clock = %q, want 0s", got)
	}
}

// TestUptimeOfAnUnparseableStartIsEmpty, so a client can tell "not known" from
// "just started".
func TestUptimeOfAnUnparseableStartIsEmpty(t *testing.T) {
	if got := computeUptimeAt("nonsense", time.Now()); got != "" {
		t.Fatalf("uptime of garbage = %q, want empty", got)
	}
}

// TestIsWindowsDesktopApp is the classification that decides whether a Windows
// port is visible at all: an app is hidden from `sonar list` and never joins a
// group. Three rules used to hide everything on a CI runner — a binary in any
// temp directory is not an installed app, an unidentified process is not
// evidence of one, and the temp rule has to be checked before the \Windows\
// and \AppData\ rules that every temp directory lives inside.
func TestIsWindowsDesktopApp(t *testing.T) {
	cases := []struct {
		name    string
		command string
		process string
		pid     int
		want    bool
	}{
		{"system idle", "", "", 0, true},
		{"system", "", "", 4, true},
		{"unknown process", "", "", 7276, false},
		{"temp build", `C:\Users\RUNNER~1\AppData\Local\Temp\sonar-itest-listener123\listener.exe 5000`, "listener.exe", 7276, false},
		{"installed app", `C:\Users\dev\AppData\Local\Programs\cursor\Cursor.exe`, "Cursor.exe", 900, true},
		{"roaming app", `C:\Users\dev\AppData\Roaming\Spotify\Spotify.exe`, "Spotify.exe", 901, true},
		{"windows service", `C:\Windows\System32\svchost.exe -k RPCSS`, "svchost.exe", 1064, true},
		{"store app", `C:\Program Files\WindowsApps\Something\app.exe`, "app.exe", 902, true},
		{"dev server", `C:\Program Files\nodejs\node.exe server.js`, "node.exe", 903, false},
		{"known app by name only", "", "chrome.exe", 904, true},
		// The exact shape of the CI regression: the CIM query withheld the
		// command line, tasklist supplied the image name, and the row has to
		// stay visible on that alone (contract §27).
		{"identity only from tasklist", "", "listener.exe", 905, false},

		// Every temp directory, not just %LOCALAPPDATA%\Temp. A scratch
		// binary under C:\Windows\Temp used to be classified as a Windows
		// system service and hidden, and one under a runner's D:\a\_temp was
		// only saved by not matching any rule at all.
		{"windows temp", `C:\Windows\Temp\sonar-itest-listener1\listener.exe 59028`, "listener.exe", 7277, false},
		{"runner temp", `D:\a\_temp\sonar-itest-listener1\listener.exe 59028`, "listener.exe", 7278, false},
		{"quoted temp path", `"C:\Users\runneradmin\AppData\Local\Temp\x\listener.exe" 59028`, "listener.exe", 7279, false},
		{"tmp directory", `C:\tmp\build\api.exe`, "api.exe", 7280, false},
		{"forward slashes", `C:/Users/dev/AppData/Local/Temp/x/listener.exe`, "listener.exe", 7281, false},
		// "temp" has to be a whole path segment: a real application does not
		// stop being one because its name looks like the word.
		{"template is not temp", `C:\Users\dev\AppData\Local\Templates\thing.exe`, "thing.exe", 7282, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWindowsDesktopApp(tc.command, tc.process, tc.pid); got != tc.want {
				t.Errorf("isWindowsDesktopApp(%q, %q, %d) = %v, want %v",
					tc.command, tc.process, tc.pid, got, tc.want)
			}
		})
	}
}

// TestParseTasklist: the fallback that names a process when the CIM query does
// not. A row it cannot read costs that row only.
func TestParseTasklist(t *testing.T) {
	out := "\"System Idle Process\",\"0\",\"Services\",\"0\",\"8 K\"\r\n" +
		"\"listener.exe\",\"7276\",\"Console\",\"1\",\"5,120 K\"\r\n" +
		"\"not a row\"\r\n" +
		"\"svchost.exe\",\"nope\",\"Services\",\"0\",\"9,000 K\"\r\n" +
		"\"node.exe\",\"3812\",\"Console\",\"1\",\"42,000 K\"\r\n"

	got := parseTasklist(out)
	want := map[int]string{0: "System Idle Process", 7276: "listener.exe", 3812: "node.exe"}
	if len(got) != len(want) {
		t.Fatalf("parseTasklist = %v, want %v", got, want)
	}
	for pid, name := range want {
		if got[pid] != name {
			t.Errorf("pid %d = %q, want %q", pid, got[pid], name)
		}
	}
}

package ports

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Enrich populates the Command field, classifies the port type, and detects
// desktop apps. This is fast and always runs.
func Enrich(pp []ListeningPort) {
	// Batch all PIDs into a single ps call for commands
	commands := batchGetCommands(pp)

	// netstat reports no process name, so on Windows a row whose command line
	// the CIM query did not answer for has no identity at all — and identity
	// is what decides whether a port is shown and which group it joins. One
	// tasklist call fills in the names, per pid, for exactly those rows.
	if runtime.GOOS == "windows" {
		fillProcessNamesWindows(pp, commands)
	}

	for i := range pp {
		if info, ok := commands[pp[i].PID]; ok {
			pp[i].Command = info.command
			// Windows learns the parent from the same CIM query that fetched
			// the command line, so the ancestry walk costs no second process
			// table. Elsewhere this is 0 and enrichDisplayNameSignals fills
			// PPID from the `ps -A` table it builds anyway.
			if info.ppid > 0 {
				pp[i].PPID = info.ppid
			}
			// started_at is never gated by --stats: the contract publishes it
			// on every row (contract §21). EnrichStats refines the same field
			// with the raw ps lstart it parses anyway.
			if pp[i].StartedAt == "" {
				pp[i].StartedAt = info.startedAt
			}
		}
		if pp[i].Type != PortTypeDocker {
			pp[i].Type = ClassifyPort(pp[i].Port)
			pp[i].IsApp = isDesktopApp(pp[i].Command, pp[i].Process, pp[i].PID)
		}
	}

	// Collect parent cmdlines, cwds and service-manager labels so DisplayName
	// can resolve meaningful names without doing any I/O itself.
	enrichDisplayNameSignals(pp)
}

// EnrichStats populates CPU, memory, threads, uptime, state, and connections.
// For Docker containers it uses pre-fetched dockerStats.
// For native processes it batches all PIDs into a single ps call.
// Called only when --stats is requested.
func EnrichStats(pp []ListeningPort, dockerStats map[string]*DockerStatsEntry) {
	// Apply Docker stats
	if dockerStats != nil {
		for i := range pp {
			if pp[i].Type == PortTypeDocker && pp[i].DockerContainer != "" {
				if stats, ok := dockerStats[pp[i].DockerContainer]; ok {
					pp[i].CPUPercent = stats.CPUPercent
					pp[i].MemoryRSS = stats.MemoryRSS
					pp[i].ThreadCount = stats.PIDs
					pp[i].State = stats.State
					pp[i].Uptime = stats.Uptime
				}
			}
		}
	}

	// Batch native process stats into a single ps call
	batchEnrichProcessStats(pp)

	// Connection counts — on Windows, fetch netstat once and reuse the output
	if runtime.GOOS == "windows" {
		out, err := exec.Command("netstat", "-ano").Output()
		if err == nil {
			output := string(out)
			for i := range pp {
				pp[i].Connections = countConnectionsNetstat(output, strconv.Itoa(pp[i].Port))
			}
		}
	} else {
		for i := range pp {
			pp[i].Connections = countConnections(pp[i].Port)
		}
	}
}

// DockerStatsEntry holds pre-fetched per-container stats.
type DockerStatsEntry struct {
	CPUPercent float64
	MemoryRSS  int64
	PIDs       int
	State      string
	Uptime     string
}

// procInfo is what one always-on ps call reports per listening process: its
// full command line and when it started. Both are unconditional — `started_at`
// is not a stat (contract §21).
type procInfo struct {
	command   string
	startedAt string // RFC3339, "" when ps did not report a parsable time
	ppid      int    // 0 when the source did not report a parent
}

// batchGetCommands fetches full command lines and start times for all PIDs in a
// single ps call.
func batchGetCommands(pp []ListeningPort) map[int]procInfo {
	pids := collectPIDs(pp)
	if len(pids) == 0 {
		return map[int]procInfo{}
	}

	pidStrs := make([]string, len(pids))
	for i, p := range pids {
		pidStrs[i] = strconv.Itoa(p)
	}

	if runtime.GOOS == "windows" {
		return batchGetCommandsWindows(pidStrs)
	}

	out, err := exec.Command("ps", "-o", "pid=,lstart=,command=", "-p", strings.Join(pidStrs, ",")).Output()
	if err != nil {
		return map[int]procInfo{}
	}
	return parsePSCommands(string(out))
}

// lstartFields is how many whitespace-separated tokens `ps -o lstart=` emits:
// "Mon Jan  2 15:04:05 2006".
const lstartFields = 5

// parsePSCommands parses `ps -o pid=,lstart=,command=` output. The command is
// everything after the fixed-width prefix, so it may contain any whitespace.
func parsePSCommands(out string) map[int]procInfo {
	result := make(map[int]procInfo)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < lstartFields+2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		lstart := strings.Join(fields[1:1+lstartFields], " ")
		command := restAfterFields(line, 1+lstartFields)
		if command == "" {
			continue
		}
		result[pid] = procInfo{command: command, startedAt: parseStartTime(lstart)}
	}
	return result
}

// restAfterFields returns line with its first n whitespace-separated fields
// removed, preserving the remainder verbatim.
func restAfterFields(line string, n int) string {
	rest := line
	for i := 0; i < n; i++ {
		rest = strings.TrimLeft(rest, " \t")
		idx := strings.IndexAny(rest, " \t")
		if idx < 0 {
			return ""
		}
		rest = rest[idx:]
	}
	return strings.TrimLeft(rest, " \t")
}

// fillProcessNamesWindows names the rows the CIM query left blank. tasklist
// needs no WMI service and no elevation, so it answers when Get-CimInstance
// does not, and a pid it cannot name simply stays unnamed: one process failing
// to resolve must not cost the others their identity.
func fillProcessNamesWindows(pp []ListeningPort, commands map[int]procInfo) {
	missing := false
	for i := range pp {
		if pp[i].Process == "" && commands[pp[i].PID].command == "" {
			missing = true
			break
		}
	}
	if !missing {
		return
	}

	out, err := exec.Command("tasklist", "/NH", "/FO", "CSV").Output()
	if err != nil {
		return
	}
	names := parseTasklist(string(out))
	for i := range pp {
		if pp[i].Process != "" {
			continue
		}
		if name, ok := names[pp[i].PID]; ok {
			pp[i].Process = name
		}
	}
}

// parseTasklist reads `tasklist /NH /FO CSV` into pid -> image name. Rows it
// cannot parse are skipped; the ones it can are still returned.
func parseTasklist(out string) map[int]string {
	names := make(map[int]string)
	r := csv.NewReader(strings.NewReader(strings.TrimSpace(out)))
	r.FieldsPerRecord = -1
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			// One unreadable line is one process we cannot name, not a reason
			// to forget the ones already read.
			var parseErr *csv.ParseError
			if errors.As(err, &parseErr) {
				continue
			}
			return names
		}
		if len(rec) < 2 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(rec[1]))
		if err != nil {
			continue
		}
		if name := strings.TrimSpace(rec[0]); name != "" {
			names[pid] = name
		}
	}
}

// batchGetCommandsWindows fetches command lines and start times via PowerShell
// Get-CimInstance on Windows.
func batchGetCommandsWindows(pidStrs []string) map[int]procInfo {
	result := make(map[int]procInfo)

	// Build WMI filter: "ProcessId=123 or ProcessId=456"
	var conditions []string
	for _, p := range pidStrs {
		conditions = append(conditions, "ProcessId="+p)
	}
	filter := strings.Join(conditions, " or ")

	// ParentProcessId rides along on the query that was already being made:
	// Windows has no `ps -A` to build a process table from, and spawning a
	// second PowerShell per scan to learn one parent pid would cost more than
	// everything else the scan does put together.
	psCmd := fmt.Sprintf(
		"Get-CimInstance Win32_Process -Filter '%s' | Select-Object ProcessId,ParentProcessId,@{N='StartedAt';E={$_.CreationDate.ToString('o')}},CommandLine | ConvertTo-Csv -NoTypeInformation",
		filter,
	)

	out, err := exec.Command("powershell", "-NoProfile", "-Command", psCmd).Output()
	if err != nil {
		return result
	}

	result = parseCIMProcesses(string(out))
	rememberScanParents(result)
	return result
}

// parseCIMProcesses reads the CSV Get-CimInstance produced.
//
// It finds its columns by name from the header rather than by position. The
// query asks for ProcessId, ParentProcessId, StartedAt and CommandLine, and
// Select-Object emits them in that order — but a positional parser turns any
// future edit to that Select-Object list, or a PowerShell that orders or names
// things differently, into silently misread rows: a parent pid read as a start
// time, a command line read from the wrong column. Reading the header is one
// map and removes the entire class.
//
// It is also tolerant per row. The previous parser called ReadAll and returned
// nothing at all when any single line failed to parse, so one process with an
// odd command line cost every other process on the machine its identity — and
// on Windows identity is what decides whether a port is shown and which group
// it joins. A row that cannot be read is skipped; the ones that can are kept.
//
// A row with no command line still counts: on Windows the command line is the
// field most often withheld (another user's process, a protected one), and
// dropping the row with it would throw away the parent pid and the start time
// that did come back.
func parseCIMProcesses(out string) map[int]procInfo {
	result := map[int]procInfo{}
	text := strings.TrimSpace(strings.TrimPrefix(out, "\ufeff"))
	if text == "" {
		return result
	}
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	var cols map[string]int
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return result
		}
		if err != nil {
			var parseErr *csv.ParseError
			if errors.As(err, &parseErr) {
				continue // one unreadable line, not the end of the batch
			}
			return result
		}
		if len(rec) == 0 {
			continue
		}
		// PowerShell 5.1 emits a `#TYPE …` line unless -NoTypeInformation is
		// passed. It is, but skipping the line costs nothing and a missing
		// flag would otherwise be read as the header.
		if strings.HasPrefix(strings.TrimSpace(rec[0]), "#TYPE") {
			continue
		}
		if cols == nil {
			cols = cimColumns(rec)
			if _, ok := cols["processid"]; !ok {
				return result // not a header we understand
			}
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(field(rec, cols, "processid")))
		if err != nil || pid <= 0 {
			continue
		}
		ppid, _ := strconv.Atoi(strings.TrimSpace(field(rec, cols, "parentprocessid")))
		cmd := strings.TrimSpace(field(rec, cols, "commandline"))
		if cmd == "" && ppid <= 0 {
			continue
		}
		result[pid] = procInfo{
			command:   cmd,
			startedAt: parseStartTime(field(rec, cols, "startedat")),
			ppid:      ppid,
		}
	}
}

// cimColumns maps a CSV header to column indexes, lowercased and trimmed so
// the lookup does not depend on PowerShell's capitalisation.
func cimColumns(header []string) map[string]int {
	cols := make(map[string]int, len(header))
	for i, name := range header {
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")))
		if key == "" {
			continue
		}
		if _, taken := cols[key]; !taken {
			cols[key] = i
		}
	}
	return cols
}

// field reads a named column, returning "" when the header did not have it or
// the row is short.
func field(rec []string, cols map[string]int, name string) string {
	i, ok := cols[name]
	if !ok || i >= len(rec) {
		return ""
	}
	return rec[i]
}

// batchEnrichProcessStats fetches CPU, memory, state, uptime for all non-Docker
// ports in a single ps call (or PowerShell on Windows).
func batchEnrichProcessStats(pp []ListeningPort) {
	var nativePorts []*ListeningPort
	for i := range pp {
		if pp[i].Type != PortTypeDocker && pp[i].PID > 0 {
			nativePorts = append(nativePorts, &pp[i])
		}
	}
	if len(nativePorts) == 0 {
		return
	}

	pidStrs := make([]string, len(nativePorts))
	for i, p := range nativePorts {
		pidStrs[i] = strconv.Itoa(p.PID)
	}

	// Build PID -> port lookup
	pidMap := make(map[int]*ListeningPort)
	for _, p := range nativePorts {
		pidMap[p.PID] = p
	}

	if runtime.GOOS == "windows" {
		batchEnrichProcessStatsWindows(pidStrs, pidMap)
		return
	}

	var out []byte
	var err error
	if runtime.GOOS == "darwin" {
		out, err = exec.Command("ps", "-o", "pid=,%cpu=,rss=,state=,lstart=", "-p", strings.Join(pidStrs, ",")).Output()
	} else {
		out, err = exec.Command("ps", "-o", "pid=,%cpu=,rss=,nlwp=,state=,lstart=", "-p", strings.Join(pidStrs, ",")).Output()
	}
	if err != nil {
		return
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		p, ok := pidMap[pid]
		if !ok {
			continue
		}
		// Parse remaining fields (skip pid)
		rest := fields[1:]
		if runtime.GOOS == "darwin" {
			parseDarwinStats(p, rest)
			p.ThreadCount = countThreadsDarwin(p.PID)
		} else {
			parseLinuxStats(p, rest)
		}
	}
}

// batchEnrichProcessStatsWindows uses PowerShell Get-Process to fetch stats.
func batchEnrichProcessStatsWindows(pidStrs []string, pidMap map[int]*ListeningPort) {
	psCmd := fmt.Sprintf(
		"Get-Process -Id %s -ErrorAction SilentlyContinue | Select-Object Id,CPU,WorkingSet64,@{N='ThreadCount';E={$_.Threads.Count}},@{N='StartTime';E={$_.StartTime.ToString('o')}} | ConvertTo-Csv -NoTypeInformation",
		strings.Join(pidStrs, ","),
	)

	out, err := exec.Command("powershell", "-NoProfile", "-Command", psCmd).Output()
	if err != nil {
		return
	}

	r := csv.NewReader(strings.NewReader(strings.TrimSpace(string(out))))
	records, err := r.ReadAll()
	if err != nil {
		return
	}

	// CSV columns: "Id","CPU","WorkingSet64","ThreadCount","StartTime"
	for i, record := range records {
		if i == 0 {
			continue // skip header
		}
		if len(record) < 5 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(record[0]))
		if err != nil {
			continue
		}
		p, ok := pidMap[pid]
		if !ok {
			continue
		}
		parseWindowsStats(p, record[1:])
	}
}

// collectPIDs returns unique non-zero PIDs from the port list.
func collectPIDs(pp []ListeningPort) []int {
	seen := make(map[int]bool)
	var pids []int
	for _, p := range pp {
		if p.PID > 0 && !seen[p.PID] {
			seen[p.PID] = true
			pids = append(pids, p.PID)
		}
	}
	return pids
}

// isDesktopApp detects if the command belongs to a desktop application or
// OS-level system service that is not relevant to development.
func isDesktopApp(command string, process string, pid int) bool {
	if runtime.GOOS == "windows" {
		return isWindowsDesktopApp(command, process, pid)
	}

	// macOS / Linux
	if command == "" {
		return false
	}
	if strings.Contains(command, ".app/") {
		return true
	}
	if strings.HasPrefix(command, "/System/Library/") || strings.HasPrefix(command, "/usr/libexec/") {
		return true
	}
	return false
}

// isWindowsDesktopApp detects Windows desktop apps and system services.
// PID 0 (System Idle) and PID 4 (System) own ports like 135, 139, 445.
//
// Everything this reports is hidden from `sonar list` unless --all is passed,
// and hidden rows never join a group, so the two guesses here are not
// symmetric: showing a system service is noise, hiding a dev server is a
// broken tool. Both rules below are written to err towards showing.
func isWindowsDesktopApp(command string, process string, pid int) bool {
	if pid == 0 || pid == 4 {
		return true
	}

	lower := strings.ToLower(command)
	if lower == "" {
		lower = strings.ToLower(process)
	}
	if lower == "" {
		// Nothing is known about this process. That is not evidence of a
		// system service: on Windows the identity comes from one CIM query,
		// and when it answers for nothing — no PowerShell, no WMI, a policy in
		// the way — reading its silence as "everything is a desktop app" hid
		// every port on the machine, dev servers included.
		return false
	}

	// A binary running out of a temp directory is a scratch build, a one-off
	// download or a test fixture. None of them is an installed application,
	// and this is checked first because every temp directory lives inside a
	// directory one of the rules below would otherwise claim:
	// %LOCALAPPDATA%\Temp is under \AppData\, C:\Windows\Temp is under
	// \Windows\, and a runner's D:\a\_temp is neither. Getting the order
	// wrong hides exactly the ports a developer just started — which is how a
	// listener built into the CI runner's temp directory vanished from
	// `sonar list` while wininit.exe stayed.
	if isWindowsTempPath(lower) {
		return false
	}
	// Windows system services
	if strings.Contains(lower, `\windows\`) {
		return true
	}
	// User-installed desktop apps: AppData\Local houses Discord, Cursor,
	// Slack and the rest.
	if strings.Contains(lower, `\appdata\`) {
		return true
	}
	// Microsoft Store apps
	if strings.Contains(lower, `\windowsapps\`) {
		return true
	}
	// Known desktop app executable names (for cases where only the process name is available)
	knownApps := []string{
		"discord", "cursor", "slack", "spotify", "figma", "zoom",
		"teams", "onedrive", "dropbox", "githubdesktop", "notion",
		"telegram", "whatsapp", "1password", "bitwarden",
		"chrome", "firefox", "msedge", "brave", "opera",
		"explorer", "searchhost", "widgets",
	}
	// Extract the base executable name without .exe
	baseName := strings.ToLower(process)
	if i := strings.LastIndex(baseName, `\`); i >= 0 {
		baseName = baseName[i+1:]
	}
	baseName = strings.TrimSuffix(baseName, ".exe")
	// Also strip any trailing quote artifacts from netstat parsing
	baseName = strings.TrimRight(baseName, `"`)

	for _, app := range knownApps {
		if strings.Contains(baseName, app) {
			return true
		}
	}
	return false
}

// isWindowsTempPath reports whether an already-lowercased command line runs out
// of a temporary directory.
//
// Two ways in, because neither alone is enough. A path segment literally called
// `temp` or `tmp` covers %LOCALAPPDATA%\Temp, C:\Windows\Temp and the
// `\_temp` a CI runner uses, whatever drive they sit on. And this process's own
// os.TempDir() covers a TMP pointed somewhere with no such name in it at all —
// the daemon and the listeners it is looking at share a machine, so its idea of
// "temporary" is theirs.
func isWindowsTempPath(lower string) bool {
	normalized := strings.ReplaceAll(lower, "/", `\`)
	for _, seg := range strings.Split(normalized, `\`) {
		// A runner's `_temp` counts; `template` and `tempo` do not.
		seg = strings.TrimPrefix(seg, "_")
		if seg == "temp" || seg == "tmp" {
			return true
		}
	}
	if dir := strings.ToLower(strings.ReplaceAll(os.TempDir(), "/", `\`)); dir != "" && dir != `\` {
		if strings.Contains(normalized, strings.TrimSuffix(dir, `\`)+`\`) {
			return true
		}
	}
	return false
}

// ClassifyPort returns PortTypeSystem for well-known ports (<1024), else PortTypeUser.
func ClassifyPort(port int) PortType {
	if port < 1024 {
		return PortTypeSystem
	}
	return PortTypeUser
}

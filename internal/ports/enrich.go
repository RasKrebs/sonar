package ports

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
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

	r := csv.NewReader(strings.NewReader(strings.TrimSpace(string(out))))
	records, err := r.ReadAll()
	if err != nil {
		return result
	}

	result = parseCIMProcesses(records)
	rememberScanParents(result)
	return result
}

// parseCIMProcesses reads the CSV Get-CimInstance produced. Columns:
// "ProcessId","ParentProcessId","StartedAt","CommandLine".
//
// A row with no command line still counts: on Windows the command line is the
// field most often withheld (another user's process, a protected one), and
// dropping the row with it would throw away the parent pid and the start time
// that did come back.
func parseCIMProcesses(records [][]string) map[int]procInfo {
	result := make(map[int]procInfo, len(records))
	for i, record := range records {
		if i == 0 {
			continue // skip header
		}
		if len(record) < 4 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(record[0]))
		if err != nil {
			continue
		}
		ppid, _ := strconv.Atoi(strings.TrimSpace(record[1]))
		cmd := strings.TrimSpace(record[3])
		if cmd == "" && ppid <= 0 {
			continue
		}
		result[pid] = procInfo{command: cmd, startedAt: parseStartTime(record[2]), ppid: ppid}
	}
	return result
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

	// Windows system services
	if strings.Contains(lower, `\windows\`) {
		return true
	}
	// User-installed desktop apps (AppData\Local houses Discord, Cursor, Slack,
	// etc.), except the temp directory: %LOCALAPPDATA%\Temp is where every
	// scratch build and one-off binary runs from, and none of them is an
	// installed application.
	if strings.Contains(lower, `\appdata\`) && !strings.Contains(lower, `\appdata\local\temp\`) {
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

// ClassifyPort returns PortTypeSystem for well-known ports (<1024), else PortTypeUser.
func ClassifyPort(port int) PortType {
	if port < 1024 {
		return PortTypeSystem
	}
	return PortTypeUser
}

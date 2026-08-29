package ports

import (
	"bufio"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Scan discovers all TCP ports in LISTEN state.
func Scan() ([]ListeningPort, error) {
	switch runtime.GOOS {
	case "darwin":
		return scanLsof()
	case "linux":
		return scanSS()
	case "windows":
		return scanNetstat()
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func scanLsof() ([]ListeningPort, error) {
	out, err := exec.Command("lsof", "-iTCP", "-sTCP:LISTEN", "-n", "-P").CombinedOutput()
	if err != nil && len(out) == 0 {
		// lsof exits non-zero when it can't inspect some processes (e.g. non-root),
		// but still outputs the ports it can see. Only fail if there's no output at all.
		return nil, nil
	}

	return parseLsof(string(out)), nil
}

// parseLsof parses the output of lsof -iTCP -sTCP:LISTEN -n -P into ListeningPort entries.
func parseLsof(output string) []ListeningPort {
	seen := make(map[string]bool)
	var results []ListeningPort

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Scan() // skip header
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 9 {
			continue
		}

		process := fields[0]
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		user := fields[2]

		// Determine IP version from the TYPE field (IPv4/IPv6)
		ipVersion := "IPv4"
		if fields[4] == "IPv6" {
			ipVersion = "IPv6"
		}

		// NAME field is like *:3000 or 127.0.0.1:3000
		name := fields[8]
		idx := strings.LastIndex(name, ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(name[idx+1:])
		if err != nil {
			continue
		}

		bindAddr := name[:idx]
		if bindAddr == "*" {
			bindAddr = "0.0.0.0"
		}

		key := fmt.Sprintf("%d:%s", port, bindAddr)
		if seen[key] {
			continue
		}
		seen[key] = true

		results = append(results, ListeningPort{
			Port:        port,
			PID:         pid,
			Process:     process,
			User:        user,
			BindAddress: bindAddr,
			IPVersion:   ipVersion,
		})
	}

	return results
}

func scanNetstat() ([]ListeningPort, error) {
	out, err := exec.Command("netstat", "-ano").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("netstat: %w\n%s", err, out)
	}

	return parseNetstat(string(out)), nil
}

// parseNetstat parses the output of netstat -ano into ListeningPort entries.
// Expected format:
//
//	Proto  Local Address          Foreign Address        State           PID
//	TCP    0.0.0.0:8000           0.0.0.0:0              LISTENING       12345
func parseNetstat(output string) []ListeningPort {
	seen := make(map[string]bool)
	var results []ListeningPort

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		proto := strings.ToUpper(fields[0])
		if proto != "TCP" && proto != "TCPV6" {
			continue
		}

		state := strings.ToUpper(fields[3])
		if state != "LISTENING" {
			continue
		}

		local := fields[1]
		idx := strings.LastIndex(local, ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(local[idx+1:])
		if err != nil {
			continue
		}

		pid, err := strconv.Atoi(fields[4])
		if err != nil {
			continue
		}

		bindAddr := local[:idx]
		ipVersion := "IPv4"
		if proto == "TCPV6" || strings.Contains(bindAddr, "[") {
			ipVersion = "IPv6"
		}
		if bindAddr == "0.0.0.0" || bindAddr == "[::]" {
			bindAddr = "0.0.0.0"
		}

		key := fmt.Sprintf("%d:%s", port, bindAddr)
		if seen[key] {
			continue
		}
		seen[key] = true

		results = append(results, ListeningPort{
			Port:        port,
			PID:         pid,
			BindAddress: bindAddr,
			IPVersion:   ipVersion,
		})
	}

	return results
}

func scanSS() ([]ListeningPort, error) {
	out, err := exec.Command("ss", "-tlnp").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ss: %w\n%s", err, out)
	}

	return parseSS(string(out)), nil
}

// parseSS parses the output of ss -tlnp into ListeningPort entries.
func parseSS(output string) []ListeningPort {
	seen := make(map[string]bool)
	var results []ListeningPort

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Scan() // skip header
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		// Local address is field 3, like *:3000 or 0.0.0.0:3000
		local := fields[3]
		idx := strings.LastIndex(local, ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(local[idx+1:])
		if err != nil {
			continue
		}

		bindAddr := local[:idx]
		if bindAddr == "*" {
			bindAddr = "0.0.0.0"
		}

		key := fmt.Sprintf("%d:%s", port, bindAddr)
		if seen[key] {
			continue
		}
		seen[key] = true

		ipVersion := "IPv4"
		if strings.Contains(bindAddr, "[") {
			ipVersion = "IPv6"
		}

		pid, process := parseSSUsers(line)

		results = append(results, ListeningPort{
			Port:        port,
			PID:         pid,
			Process:     process,
			BindAddress: bindAddr,
			IPVersion:   ipVersion,
		})
	}

	return results
}

// parseSSUsers extracts the process name and PID from an ss -p users: blob.
//
// The blob must be read from the raw line, not from strings.Fields tokens.
// Linux comm values can contain spaces (Next.js sets process.title to
// "next-server (v…)", truncated to 15 bytes as "next-server (v1"), and
// splitting on whitespace separates "users:(("name" from ",pid=N,fd=…)".
func parseSSUsers(line string) (pid int, process string) {
	usersIdx := strings.Index(line, "users:")
	if usersIdx < 0 {
		return 0, ""
	}
	blob := line[usersIdx:]

	if pidIdx := strings.Index(blob, "pid="); pidIdx >= 0 {
		pidStr := blob[pidIdx+4:]
		end := 0
		for end < len(pidStr) && pidStr[end] >= '0' && pidStr[end] <= '9' {
			end++
		}
		if end > 0 {
			pid, _ = strconv.Atoi(pidStr[:end])
		}
	}

	if nameStart := strings.Index(blob, `(("`); nameStart >= 0 {
		nameStr := blob[nameStart+3:]
		if nameEnd := strings.IndexByte(nameStr, '"'); nameEnd >= 0 {
			process = nameStr[:nameEnd]
		}
	}
	return pid, process
}

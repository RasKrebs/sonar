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

		// NAME field is like *:3000, 127.0.0.1:3000 or [::1]:3000
		name := fields[8]
		idx := strings.LastIndex(name, ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(name[idx+1:])
		if err != nil {
			continue
		}

		// The TYPE field (IPv4/IPv6) is the address family lsof saw; it is the
		// hint that resolves a bare "*" wildcard.
		bindAddr, ipVersion := normalizeBind(name[:idx], fields[4] == "IPv6")

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

		bindAddr, ipVersion := normalizeBind(local[:idx], proto == "TCPV6")

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

		// ss has no address-family column: the address itself is the only
		// hint, so a bare "*" is read as the IPv4 wildcard.
		bindAddr, ipVersion := normalizeBind(local[:idx], false)

		key := fmt.Sprintf("%d:%s", port, bindAddr)
		if seen[key] {
			continue
		}
		seen[key] = true

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

// parseSSUsers extracts the process name and PID from an ss -p users: blob,
// which looks like:
//
//	users:(("nginx",pid=1234,fd=6),("nginx",pid=1235,fd=6))
//
// The blob must be read from the raw line, not from strings.Fields tokens.
// Linux comm values can contain spaces (Next.js sets process.title to
// "next-server (v…)", truncated to 15 bytes as "next-server (v1"), and
// splitting on whitespace separates "users:(("name" from ",pid=N,fd=…)".
//
// The name is read first and the PID only from what follows it, so a process
// title that itself contains "pid=" cannot be mistaken for the real PID. Only
// the first entry is used; ss lists one per file descriptor.
func parseSSUsers(line string) (pid int, process string) {
	nameStart := strings.Index(line, `users:(("`)
	if nameStart < 0 {
		return 0, ""
	}
	entry := line[nameStart+len(`users:(("`):]

	nameEnd := strings.Index(entry, `",`)
	if nameEnd < 0 {
		return 0, ""
	}
	process = entry[:nameEnd]

	// iproute2 before v4.0 omits the "pid=" label and prints a bare number.
	rest := strings.TrimPrefix(entry[nameEnd+2:], "pid=")
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end > 0 {
		pid, _ = strconv.Atoi(rest[:end])
	}

	return pid, process
}

// normalizeBind turns a scanner's raw local address into the contract's
// bind_address and the ip_version that agrees with it (contract §21). The
// wildcard is "0.0.0.0" on IPv4 and "::" on IPv6 — never "0.0.0.0" paired with
// "IPv6", which is what a dual-stack listener used to report. v6 is the address
// family the scanner reported out of band (lsof's TYPE column, netstat's
// proto); it only decides a bare "*", since every other form carries its family
// in the address text.
func normalizeBind(raw string, v6 bool) (bind, ipVersion string) {
	addr := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(raw), "["), "]")
	if addr == "" || addr == "*" {
		if v6 {
			return "::", "IPv6"
		}
		return "0.0.0.0", "IPv4"
	}
	if strings.Contains(addr, ":") {
		return addr, "IPv6"
	}
	return addr, "IPv4"
}

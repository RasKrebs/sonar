package docker

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/raskrebs/sonar/internal/ports"
)

// BuildDockerGraph discovers TCP connections between Docker containers by
// reading /proc/net/tcp inside each container and matching remote IPs
// to known container addresses on shared Docker networks.
func BuildDockerGraph(listening []ports.ListeningPort) ([]ports.Connection, error) {
	// Collect Docker containers from listening ports
	var dockerPorts []ports.ListeningPort
	for _, lp := range listening {
		if lp.Type == ports.PortTypeDocker {
			dockerPorts = append(dockerPorts, lp)
		}
	}
	if len(dockerPorts) == 0 {
		return nil, nil
	}

	// Deduplicate container names
	containerNames := uniqueContainerNames(dockerPorts)

	// Get IPs for all containers on shared networks
	ipToContainer := buildIPMap(containerNames)
	if len(ipToContainer) == 0 {
		return nil, nil
	}

	// Build container name → host port and display name mappings
	containerHostPort := make(map[string]int)        // first host port per container
	containerDisplay := make(map[string]string)       // display name per container
	containerPortMap := make(map[string]map[int]int)  // container: containerPort→hostPort
	containerListening := make(map[string]map[int]bool) // container: set of listening container ports
	for _, lp := range dockerPorts {
		name := lp.DockerContainer
		if _, ok := containerHostPort[name]; !ok {
			containerHostPort[name] = lp.Port
		}
		containerDisplay[name] = lp.DisplayName()
		if containerPortMap[name] == nil {
			containerPortMap[name] = make(map[int]int)
			containerListening[name] = make(map[int]bool)
		}
		containerPortMap[name][lp.DockerContainerPort] = lp.Port
		containerListening[name][lp.DockerContainerPort] = true
	}

	// Read /proc/net/tcp from each container in parallel
	type containerConns struct {
		name    string
		entries []procNetEntry
	}

	var wg sync.WaitGroup
	ch := make(chan containerConns, len(containerNames))
	for _, name := range containerNames {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			ch <- containerConns{name: n, entries: readProcNetTCP(n)}
		}(name)
	}
	wg.Wait()
	close(ch)

	// Match connections
	type connKey struct{ from, to string }
	seen := make(map[connKey]bool)
	var connections []ports.Connection

	for result := range ch {
		fromName := result.name
		for _, e := range result.entries {
			if e.state != 1 { // ESTABLISHED
				continue
			}
			toName, ok := ipToContainer[e.remoteIP]
			if !ok || toName == fromName {
				continue
			}
			// Only match if the remote port is a known listening port,
			// not an ephemeral client port
			if !containerListening[toName][e.remotePort] {
				continue
			}
			key := connKey{fromName, toName}
			if seen[key] {
				continue
			}
			seen[key] = true

			// Resolve host port for the remote container port
			toPort := e.remotePort
			if hp, ok := containerPortMap[toName][e.remotePort]; ok {
				toPort = hp
			}

			fromPort := containerHostPort[fromName]
			fromDisplay := containerDisplay[fromName]
			toDisplay := containerDisplay[toName]
			if toDisplay == "" {
				toDisplay = toName
			}

			connections = append(connections, ports.Connection{
				FromPort:    fromPort,
				FromProcess: fromDisplay,
				ToPort:      toPort,
				ToProcess:   toDisplay,
			})
		}
	}

	return connections, nil
}

// uniqueContainerNames returns deduplicated container names from Docker listening ports.
func uniqueContainerNames(dockerPorts []ports.ListeningPort) []string {
	seen := make(map[string]bool)
	var names []string
	for _, lp := range dockerPorts {
		if !seen[lp.DockerContainer] {
			names = append(names, lp.DockerContainer)
			seen[lp.DockerContainer] = true
		}
	}
	return names
}

// buildIPMap queries docker inspect for each container's network IPs and returns
// a map of IP address → container name.
func buildIPMap(containerNames []string) map[string]string {
	if len(containerNames) == 0 {
		return nil
	}

	// docker inspect --format for all containers at once
	args := append([]string{"inspect", "--format",
		`{{.Name}}{{range $net, $cfg := .NetworkSettings.Networks}} {{$cfg.IPAddress}}{{end}}`},
		containerNames...)

	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return nil
	}

	ipMap := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[0], "/")
		for _, ip := range fields[1:] {
			if ip != "" {
				ipMap[ip] = name
			}
		}
	}
	return ipMap
}

// procNetEntry represents a parsed line from /proc/net/tcp.
type procNetEntry struct {
	remoteIP   string
	remotePort int
	state      int // 1=ESTABLISHED, 0A=LISTEN, etc.
}

// readProcNetTCP reads /proc/net/tcp inside a Docker container and returns parsed entries.
func readProcNetTCP(containerName string) []procNetEntry {
	out, err := exec.Command("docker", "exec", containerName, "cat", "/proc/net/tcp").Output()
	if err != nil {
		return nil
	}

	var entries []procNetEntry
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Scan() // skip header
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}

		state, err := strconv.ParseInt(fields[3], 16, 32)
		if err != nil {
			continue
		}

		ip, port, ok := parseHexAddr(fields[2]) // rem_address
		if !ok {
			continue
		}

		entries = append(entries, procNetEntry{
			remoteIP:   ip,
			remotePort: port,
			state:      int(state),
		})
	}
	return entries
}

// parseHexAddr parses a hex address like "070013AC:1538" into an IP string and port.
// The IP is stored as a little-endian 32-bit hex value in /proc/net/tcp.
func parseHexAddr(s string) (string, int, bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 || len(parts[0]) != 8 {
		return "", 0, false
	}

	n, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return "", 0, false
	}
	ip := fmt.Sprintf("%d.%d.%d.%d", n&0xFF, (n>>8)&0xFF, (n>>16)&0xFF, (n>>24)&0xFF)

	port, err := strconv.ParseUint(parts[1], 16, 16)
	if err != nil {
		return "", 0, false
	}

	return ip, int(port), true
}

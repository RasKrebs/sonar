package docker

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/raskrebs/sonar/internal/ports"
)

type container struct {
	name           string
	image          string
	portMappings   []portMapping
	composeService string
	composeProject string
}

type portMapping struct {
	hostPort      int
	containerPort int
}

// EnrichPorts queries Docker for running containers and enriches any ports
// that match Docker-published host ports. Fails silently if Docker is unavailable.
func EnrichPorts(pp []ports.ListeningPort) {
	containers, err := listContainers()
	if err != nil || len(containers) == 0 {
		return
	}

	// Build a map of host port -> container info
	hostPortMap := make(map[int]*container)
	mappingMap := make(map[int]portMapping)
	for i := range containers {
		for _, pm := range containers[i].portMappings {
			hostPortMap[pm.hostPort] = &containers[i]
			mappingMap[pm.hostPort] = pm
		}
	}

	for i := range pp {
		c, ok := hostPortMap[pp[i].Port]
		if !ok {
			continue
		}
		pp[i].Type = ports.PortTypeDocker
		pp[i].DockerContainer = c.name
		pp[i].DockerImage = c.image
		pp[i].DockerComposeService = c.composeService
		pp[i].DockerComposeProject = c.composeProject
		pp[i].DockerContainerPort = mappingMap[pp[i].Port].containerPort
	}
}

// StopContainer stops a Docker container by name using `docker stop`.
func StopContainer(name string) error {
	if err := exec.Command("docker", "stop", name).Run(); err != nil {
		return fmt.Errorf("failed to stop container %s: %w", name, err)
	}
	return nil
}

// listContainers runs `docker ps` and parses the output.
func listContainers() ([]container, error) {
	format := "{{.Names}}\t{{.Image}}\t{{.Ports}}\t{{.Label \"com.docker.compose.service\"}}\t{{.Label \"com.docker.compose.project\"}}"
	out, err := exec.Command("docker", "ps", "--format", format).Output()
	if err != nil {
		return nil, err
	}

	var containers []container
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 5 {
			continue
		}

		c := container{
			name:           parts[0],
			image:          parts[1],
			portMappings:   parsePorts(parts[2]),
			composeService: parts[3],
			composeProject: parts[4],
		}
		containers = append(containers, c)
	}

	return containers, nil
}

// parsePorts parses Docker port strings like "0.0.0.0:3000->80/tcp, 0.0.0.0:3001->443/tcp".
func parsePorts(raw string) []portMapping {
	if raw == "" {
		return nil
	}

	var mappings []portMapping
	for _, entry := range strings.Split(raw, ", ") {
		// Format: 0.0.0.0:3000->80/tcp or :::3000->80/tcp
		arrowIdx := strings.Index(entry, "->")
		if arrowIdx < 0 {
			continue
		}

		hostPart := entry[:arrowIdx]
		containerPart := entry[arrowIdx+2:]

		// Extract host port (after last colon)
		colonIdx := strings.LastIndex(hostPart, ":")
		if colonIdx < 0 {
			continue
		}
		hostPort, err := strconv.Atoi(hostPart[colonIdx+1:])
		if err != nil {
			continue
		}

		// Extract container port (before /tcp or /udp)
		slashIdx := strings.Index(containerPart, "/")
		cpStr := containerPart
		if slashIdx >= 0 {
			cpStr = containerPart[:slashIdx]
		}
		containerPort, err := strconv.Atoi(cpStr)
		if err != nil {
			continue
		}

		mappings = append(mappings, portMapping{
			hostPort:      hostPort,
			containerPort: containerPort,
		})
	}

	return mappings
}

package daemon

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/scanner"
	"github.com/raskrebs/sonar/internal/state"
)

// defaultLogLines is the backlog a caller gets when it does not ask for one.
// Contract §4 fixes it at 100.
const defaultLogLines = 100

// logTail is how one port's output is read: a label the client prints, the
// command that reads the last N lines, and the command that follows.
type logTail struct {
	source string
	tail   []string
	follow []string
}

// resolveLogTail decides where a port's output comes from. The ladder is the
// one `sonar logs` has always walked: a container's own log, then the files the
// process holds open, then the platform's fallback.
func resolveLogTail(row state.Port, lines int) (logTail, error) {
	if row.Docker != nil && row.Docker.Container != "" {
		n := strconv.Itoa(lines)
		return logTail{
			source: "docker:" + row.Docker.Container,
			tail:   []string{"docker", "logs", "--tail", n, row.Docker.Container},
			follow: []string{"docker", "logs", "--tail", n, "-f", row.Docker.Container},
		}, nil
	}

	if runtime.GOOS == "windows" {
		return logTail{}, rpc.NewError(rpc.CodeUnsupported,
			"log viewing is not supported on Windows for non-Docker processes", "")
	}

	if sources := ports.FindLogSources(row.PID); len(sources) > 0 {
		paths := make([]string, 0, len(sources))
		labels := make([]string, 0, len(sources))
		for _, s := range sources {
			paths = append(paths, s.Path)
			if s.FD != "" {
				labels = append(labels, fmt.Sprintf("%s (%s)", s.Path, s.FD))
				continue
			}
			labels = append(labels, s.Path)
		}
		return logTail{
			source: strings.Join(labels, ", "),
			tail:   tailArgv(lines, false, paths),
			follow: tailArgv(lines, true, paths),
		}, nil
	}

	if runtime.GOOS == "linux" {
		var paths []string
		for _, fd := range []string{"1", "2"} {
			path := fmt.Sprintf("/proc/%d/fd/%s", row.PID, fd)
			if _, err := os.Stat(path); err == nil {
				paths = append(paths, path)
			}
		}
		if len(paths) > 0 {
			return logTail{
				source: strings.Join(paths, ", "),
				tail:   tailArgv(lines, false, paths),
				follow: tailArgv(lines, true, paths),
			}, nil
		}
	}

	if ports.SupportsLogStream() {
		return logTail{
			source: "log stream",
			follow: []string{"log", "stream", "--process", strconv.Itoa(row.PID), "--style", "compact"},
		}, nil
	}

	return logTail{}, rpc.NewError(rpc.CodeNotFound,
		fmt.Sprintf("no log sources found for pid %d", row.PID),
		"restart the process with its output redirected to a file")
}

func tailArgv(lines int, follow bool, paths []string) []string {
	argv := []string{"tail", "-n", strconv.Itoa(lines)}
	if follow {
		argv = append(argv, "-f")
	}
	return append(argv, paths...)
}

// handlePortsLogs is unary with follow:false ({source, lines, truncated}) and
// streaming with follow:true, where the backlog and everything after it arrive
// as stream.chunk notifications (contract §1).
func handlePortsLogs(ctx context.Context, req *Request) (any, error) {
	var p rpc.PortsLogsParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	lines := p.Lines
	if lines <= 0 {
		lines = defaultLogLines
	}

	rows, err := readPorts(req.Runtime, scanner.Include{})
	if err != nil {
		return nil, err
	}
	row, err := resolvePort(rows, p.Selector)
	if err != nil {
		return nil, err
	}
	tail, err := resolveLogTail(row, lines)
	if err != nil {
		return nil, err
	}

	if !p.Follow {
		if len(tail.tail) == 0 {
			return rpc.PortsLogsResult{Source: tail.source, Lines: []string{}}, nil
		}
		out, err := runLogCommand(ctx, tail.tail)
		if err != nil {
			return nil, err
		}
		return rpc.PortsLogsResult{
			Source:    tail.source,
			Lines:     out,
			Truncated: len(out) >= lines,
		}, nil
	}

	initial := rpc.PortsLogsResult{Source: tail.source, Lines: []string{}}
	return StartStream(ctx, req, initial, func(ctx context.Context, s *Stream) (any, error) {
		return nil, streamLines(ctx, tail.follow, func(line string) error {
			return s.Send(rpc.PortsLogsChunk{Source: tail.source, Line: line})
		})
	})
}

// runLogCommand reads a bounded amount of output from a one-shot tail.
func runLogCommand(ctx context.Context, argv []string) ([]string, error) {
	if _, err := exec.LookPath(argv[0]); err != nil {
		return nil, rpc.NewError(rpc.CodeUnsupported, argv[0]+" is not on PATH", "")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, rpc.NewError(rpc.CodeInternal,
			fmt.Sprintf("reading logs with %s: %v", argv[0], err), "")
	}
	text := strings.TrimSuffix(string(out), "\n")
	if text == "" {
		return []string{}, nil
	}
	return strings.Split(text, "\n"), nil
}

// streamLines runs a follow command and hands every line to emit until the
// stream is cancelled, the client disconnects or the command exits. Killing the
// child is the context's job: exec.CommandContext does it on cancellation.
func streamLines(ctx context.Context, argv []string, emit func(string) error) error {
	if len(argv) == 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return rpc.NewError(rpc.CodeUnsupported, argv[0]+" is not on PATH", "")
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return rpc.NewError(rpc.CodeInternal, "opening the log pipe: "+err.Error(), "")
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return rpc.NewError(rpc.CodeInternal,
			fmt.Sprintf("starting %s: %v", argv[0], err), "")
	}
	defer func() {
		_ = cmd.Wait()
	}()

	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if err := emit(sc.Text()); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil && !errIsClosed(err) {
		return err
	}
	return ctx.Err()
}

func errIsClosed(err error) bool {
	return err == io.ErrClosedPipe || strings.Contains(err.Error(), "file already closed")
}

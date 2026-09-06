package display

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// ungroupedLabel heads the ports that resolve to no group at all.
const ungroupedLabel = "ungrouped"

// RenderTree writes the grouped tree view: one block per group, its ports
// underneath, and the ungrouped ports last.
//
// gg supplies each group's status, root directory and service names; it is the
// output of groups.Groups over the same scan. Ports carry the group name the
// resolver gave them.
func RenderTree(w io.Writer, pp []ports.ListeningPort, gg []state.Group) {
	if len(pp) == 0 {
		fmt.Fprintln(w, "No listening ports found.")
		return
	}
	pp = dedupeFamilies(pp)

	byGroup := map[string][]ports.ListeningPort{}
	for _, p := range pp {
		byGroup[p.Group] = append(byGroup[p.Group], p)
	}
	meta := map[string]state.Group{}
	for _, g := range gg {
		meta[g.Name] = g
	}
	serviceOf := map[int]string{}
	for _, g := range gg {
		for _, s := range g.Services {
			if s.PortActual != nil {
				serviceOf[*s.PortActual] = s.Name
			}
		}
	}

	names := make([]string, 0, len(byGroup))
	for name := range byGroup {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if _, ok := byGroup[""]; ok {
		names = append(names, "")
	}

	// One shared column layout across every group keeps the tree readable.
	portW, nameW, detailW := 0, 0, 0
	for _, p := range pp {
		portW = max(portW, len(fmt.Sprintf("%d", p.Port)))
		nameW = max(nameW, len(treeName(p, serviceOf)))
		detailW = max(detailW, len(treeDetail(p, serviceOf)))
	}
	const indent = 3 // "├─ "
	const gap = 2
	urlCol := indent + portW + gap + nameW + gap + detailW + gap

	for gi, name := range names {
		if gi > 0 {
			fmt.Fprintln(w)
		}
		rows := byGroup[name]
		sort.Slice(rows, func(i, j int) bool { return rows[i].Port < rows[j].Port })
		fmt.Fprintln(w, groupHeader(name, meta[name], rows, urlCol))

		for i, p := range rows {
			branch := "├─ "
			if i == len(rows)-1 {
				branch = "└─ "
			}
			line := branch +
				padRight(BoldCyan(fmt.Sprintf("%d", p.Port)), portW) + strings.Repeat(" ", gap) +
				padRight(colorName(p, treeName(p, serviceOf)), nameW) + strings.Repeat(" ", gap) +
				padRight(Dim(treeDetail(p, serviceOf)), detailW) + strings.Repeat(" ", gap) +
				Underline(p.URL())
			fmt.Fprintln(w, strings.TrimRight(line, " "))
		}
	}

	fmt.Fprintf(w, "\n%s\n", Dim(treeSummary(len(pp), len(names), byGroup)))
}

// dedupeFamilies collapses the rows one process contributes for one port.
//
// A service bound on both families is two rows — 127.0.0.1 and [::1] — and the
// table view is right to show both: they are two sockets, and which one a
// client reaches matters. The tree is a different question. It answers "what is
// running, and which project does it belong to", and printing
//
//	├─ 49665  wininit.exe   http://localhost:49665
//	├─ 49665  wininit.exe   http://localhost:49665
//
// answers it twice, inflates every group's port count, and is worst exactly
// where the tree is most useful — a dual-stack dev server under its repo.
//
// The first row for a (port, pid) pair wins, so the ordering the scanner
// produces decides which address is shown, and `--json` and the table keep
// every row. A pid of 0 (an identity the scanner could not resolve) still
// collapses per port: two families of one unknown listener are one listener.
func dedupeFamilies(pp []ports.ListeningPort) []ports.ListeningPort {
	type key struct {
		port int
		pid  int
	}
	seen := make(map[key]bool, len(pp))
	out := make([]ports.ListeningPort, 0, len(pp))
	for _, p := range pp {
		k := key{port: p.Port, pid: p.PID}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, p)
	}
	return out
}

// groupHeader is the "name  (n ports, status)" line, with the group's root
// directory aligned under the URL column.
func groupHeader(name string, g state.Group, rows []ports.ListeningPort, urlCol int) string {
	label := name
	if label == "" {
		label = ungroupedLabel
	}
	status := g.Status
	if name == "" {
		status = ""
	}
	if status == "" && name != "" {
		status = "running"
	}

	count := fmt.Sprintf("%d %s", len(rows), plural(len(rows), "port"))
	suffix := "(" + count + ")"
	if status != "" {
		suffix = "(" + count + ", " + colorStatus(status) + ")"
	}
	head := Bold(label) + "  " + suffix

	root := ""
	if g.RootDir != nil {
		root = shortenHome(*g.RootDir)
	}
	if root == "" {
		return head
	}
	if visibleLen(head) >= urlCol {
		return head + "  " + Dim(root)
	}
	return padRight(head, urlCol) + Dim(root)
}

func treeSummary(portCount, groupCount int, byGroup map[string][]ports.ListeningPort) string {
	named := groupCount
	if _, ok := byGroup[""]; ok {
		named--
	}
	return fmt.Sprintf("%d %s in %d %s",
		portCount, plural(portCount, "port"), named, plural(named, "group"))
}

// treeName is the service name from the group's config when the port maps to
// one, otherwise the scanner's display name.
func treeName(p ports.ListeningPort, serviceOf map[int]string) string {
	if svc, ok := serviceOf[p.Port]; ok && svc != "" {
		return svc
	}
	if name := p.DisplayName(); name != "" {
		return name
	}
	if svc := ports.ServiceName(p.Port); svc != "" {
		return "~" + svc
	}
	return p.Process
}

// treeDetail is the second column: what is actually running, when the name
// column shows something else.
func treeDetail(p ports.ListeningPort, serviceOf map[int]string) string {
	if p.DockerImage != "" {
		return p.DockerImage
	}
	name := treeName(p, serviceOf)
	if display := p.DisplayName(); display != "" && display != name {
		return display
	}
	if p.Process != "" && p.Process != name {
		return p.Process
	}
	return ""
}

func colorName(p ports.ListeningPort, name string) string {
	switch p.Type {
	case ports.PortTypeDocker:
		return Magenta(name)
	case ports.PortTypeSystem:
		return Yellow(name)
	default:
		return Green(name)
	}
}

func colorStatus(status string) string {
	switch status {
	case "running":
		return Green(status)
	case "partial":
		return Yellow(status)
	case "stopped":
		return Dim(status)
	default:
		return status
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// shortenHome rewrites a path under the user's home directory as ~/….
func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || path == home {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil &&
		rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "~" + string(filepath.Separator) + rel
	}
	return path
}

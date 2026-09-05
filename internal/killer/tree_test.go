package killer

import (
	"reflect"
	"testing"
)

// npmTree is the tree the roadmap's demo names: a shell wrapping npm, which
// forks vite, which forks two esbuild workers.
//
//	1 ── 100 sh ── 200 npm ── 300 vite ── 400 esbuild
//	                                   └─ 401 esbuild
//	              └─ 201 tsc
func npmTree() ProcessTable {
	return ProcessTable{
		100: {PID: 100, PPID: 1, Command: "/bin/sh -c npm run dev"},
		200: {PID: 200, PPID: 100, Command: "npm run dev"},
		201: {PID: 201, PPID: 100, Command: "/usr/local/bin/tsc --watch"},
		300: {PID: 300, PPID: 200, Command: "node /repo/node_modules/.bin/vite"},
		400: {PID: 400, PPID: 300, Command: "/repo/node_modules/@esbuild/bin/esbuild --service"},
		401: {PID: 401, PPID: 300, Command: "/repo/node_modules/@esbuild/bin/esbuild --service"},
	}
}

func TestDescendantsOrdersChildrenBeforeParents(t *testing.T) {
	got := npmTree().Descendants(100)
	want := []int{400, 401, 300, 200, 201, 100}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Descendants(100) = %v, want %v", got, want)
	}

	// Every parent must appear after all of its descendants.
	pos := map[int]int{}
	for i, pid := range got {
		pos[pid] = i
	}
	for pid, p := range npmTree() {
		if _, ok := pos[p.PPID]; !ok {
			continue
		}
		if pos[pid] > pos[p.PPID] {
			t.Errorf("child %d signalled after parent %d", pid, p.PPID)
		}
	}
}

func TestDescendantsOfLeafAndUnknown(t *testing.T) {
	if got := npmTree().Descendants(400); !reflect.DeepEqual(got, []int{400}) {
		t.Errorf("leaf: got %v, want [400]", got)
	}
	// A pid the table never saw is still a valid single-process target.
	if got := npmTree().Descendants(999); !reflect.DeepEqual(got, []int{999}) {
		t.Errorf("unknown pid: got %v, want [999]", got)
	}
	if got := npmTree().Descendants(0); got != nil {
		t.Errorf("pid 0: got %v, want nil", got)
	}
}

func TestDescendantsSurvivesCycles(t *testing.T) {
	// A table where two pids claim each other as parent must not spin.
	cyclic := ProcessTable{
		10: {PID: 10, PPID: 11},
		11: {PID: 11, PPID: 10},
	}
	got := cyclic.Descendants(10)
	if len(got) != 2 || got[len(got)-1] != 10 {
		t.Fatalf("Descendants(10) = %v, want both pids ending at the root", got)
	}
}

func TestAncestors(t *testing.T) {
	got := npmTree().Ancestors(400)
	want := []int{300, 200, 100}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Ancestors(400) = %v, want %v", got, want)
	}
	if got := npmTree().Ancestors(100); len(got) != 0 {
		t.Fatalf("Ancestors(100) = %v, want none (pid 1 is not an ancestor we signal)", got)
	}
}

func TestName(t *testing.T) {
	table := npmTree()
	cases := map[int]string{100: "sh", 200: "npm", 300: "node", 400: "esbuild", 999: ""}
	for pid, want := range cases {
		if got := table.Name(pid); got != want {
			t.Errorf("Name(%d) = %q, want %q", pid, got, want)
		}
	}
}

func TestParseProcessTable(t *testing.T) {
	out := `
  100     1 /bin/sh -c npm run dev
  200   100 npm run dev
  bad line
  300   200 node /repo/node_modules/.bin/vite --port 3000
`
	table := parseProcessTable(out)
	if len(table) != 3 {
		t.Fatalf("parsed %d rows, want 3: %v", len(table), table)
	}
	if got := table[300]; got.PPID != 200 || got.Command != "node /repo/node_modules/.bin/vite --port 3000" {
		t.Fatalf("row 300 = %+v", got)
	}
}

package doctor

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParsePositionPointsAtTheUnclosedBracket(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantLine int
		wantCol  int
	}{
		{
			// The config the step was written for.
			name: "flow sequence never closed", src: "list: [broken",
			wantLine: 1, wantCol: 7,
		},
		{
			// go-yaml blames line 1 here; the bracket is on line 2.
			name: "unclosed bracket below the reported line",
			src:  "a: 1\nb: [1,\nc: 2\n", wantLine: 2, wantCol: 4,
		},
		{
			name: "unclosed mapping", src: "a: {x: 1\n", wantLine: 1, wantCol: 4,
		},
		{
			name:     "the innermost one wins",
			src:      "a: [1, {b: 2\n",
			wantLine: 1, wantCol: 8,
		},
		{
			name:     "a bracket inside a quoted scalar is not an opener",
			src:      "a: \"[\"\nb: [1\n",
			wantLine: 2, wantCol: 4,
		},
		{
			name:     "a bracket in a comment is not an opener",
			src:      "a: 1 # [\nb: [1\n",
			wantLine: 2, wantCol: 4,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var node yaml.Node
			err := yaml.Unmarshal([]byte(tc.src), &node)
			if err == nil {
				t.Fatalf("%q parsed cleanly; the fixture is not a syntax error", tc.src)
			}
			pos := parsePosition(tc.src, err)
			if pos.Line != tc.wantLine || pos.Col != tc.wantCol {
				t.Errorf("position = %d:%d, want %d:%d (yaml said %v)",
					pos.Line, pos.Col, tc.wantLine, tc.wantCol, err)
			}
		})
	}
}

func TestParsePositionFallsBackToTheParsersLine(t *testing.T) {
	const src = "a: *nope\n"
	var node yaml.Node
	err := yaml.Unmarshal([]byte(src), &node)
	if err == nil {
		t.Fatal("an unknown anchor should not parse")
	}
	// go-yaml reports this one without a line at all, so there is nothing to
	// point at and the check falls back to naming the file.
	if pos := parsePosition(src, err); pos.Line != 0 {
		t.Errorf("position = %d:%d, want no position", pos.Line, pos.Col)
	}
}

func TestCaretMarksTheColumn(t *testing.T) {
	got := caret("list: [broken", Position{Line: 1, Col: 7})
	want := "  list: [broken\n        ^"
	if got != want {
		t.Errorf("caret =\n%s\nwant\n%s", got, want)
	}
}

func TestYamlMessageDropsTheRedundantPrefixes(t *testing.T) {
	var node yaml.Node
	err := yaml.Unmarshal([]byte("list: [broken"), &node)
	got := yamlMessage(err)
	if strings.Contains(got, "yaml:") || strings.Contains(got, "line 1") {
		t.Errorf("message = %q, want it without the yaml: and line prefixes", got)
	}
	if !strings.Contains(got, "','") {
		t.Errorf("message = %q, want it to keep what the parser said", got)
	}
}

package doctor

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// go-yaml reports a syntax error as "yaml: line N: <what>" and nothing else:
// no column, and N is where the parser gave up rather than where the mistake
// is. `list: [broken` is reported at line 1 whichever line the bracket was
// opened on, so a user with a long config is told the file is broken and left
// to find the spot.
//
// So the doctor finds the spot itself. A flow collection that is still open at
// the end of the document is the mistake, and its opening bracket is both the
// line and the column worth pointing at; anything else falls back to the
// parser's own line. Either way the caller gets a position it can print with a
// caret under it, which is what makes `config_parses` actionable.

var yamlLinePattern = regexp.MustCompile(`line (\d+):`)

// Position is a 1-based location in a source file.
type Position struct {
	Line int
	Col  int
}

// parsePosition locates a YAML parse failure in src. A zero Line means the
// error carried no position at all.
func parsePosition(src string, err error) Position {
	if line, col, ok := unclosedFlow(src); ok {
		return Position{Line: line, Col: col}
	}
	if err == nil {
		return Position{}
	}
	m := yamlLinePattern.FindStringSubmatch(err.Error())
	if m == nil {
		return Position{}
	}
	line, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		return Position{}
	}
	return Position{Line: line, Col: 1}
}

// yamlMessage is a go-yaml error without the "yaml:" prefix and without the
// "line N:" it repeats: the position is printed separately, and more precisely.
func yamlMessage(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	return strings.TrimSpace(yamlLinePattern.ReplaceAllString(msg, ""))
}

// unclosedFlow returns the position of the innermost `[` or `{` that is never
// closed, skipping comments and quoted scalars.
func unclosedFlow(src string) (int, int, bool) {
	type mark struct {
		line, col int
		open      byte
	}
	var stack []mark

	line, col := 1, 1
	var inSingle, inDouble, inComment bool
	for i := 0; i < len(src); i++ {
		ch := src[i]
		switch {
		case ch == '\n':
			line, col = line+1, 0 // col is incremented at the bottom
			inComment, inSingle, inDouble = false, false, false
		case inComment:
		case inSingle:
			if ch == '\'' {
				if i+1 < len(src) && src[i+1] == '\'' {
					i, col = i+1, col+1 // an escaped quote, still inside
					break
				}
				inSingle = false
			}
		case inDouble:
			if ch == '\\' && i+1 < len(src) {
				i, col = i+1, col+1
				break
			}
			if ch == '"' {
				inDouble = false
			}
		case ch == '#' && (i == 0 || src[i-1] == ' ' || src[i-1] == '\t' || src[i-1] == '\n'):
			inComment = true
		case ch == '\'':
			inSingle = true
		case ch == '"':
			inDouble = true
		case ch == '[' || ch == '{':
			stack = append(stack, mark{line: line, col: col, open: ch})
		case ch == ']' || ch == '}':
			want := byte('[')
			if ch == '}' {
				want = '{'
			}
			if n := len(stack); n > 0 && stack[n-1].open == want {
				stack = stack[:n-1]
			}
		}
		col++
	}
	if len(stack) == 0 {
		return 0, 0, false
	}
	top := stack[len(stack)-1]
	return top.line, top.col, true
}

// caret renders the offending line with a marker under the column, so the
// detail of a failing config_parses check reads like a compiler error.
func caret(src string, pos Position) string {
	if pos.Line <= 0 {
		return ""
	}
	lines := strings.Split(src, "\n")
	if pos.Line > len(lines) {
		return ""
	}
	text := strings.TrimRight(lines[pos.Line-1], "\r")
	if pos.Col <= 0 || pos.Col > len(text)+1 {
		return fmt.Sprintf("  %s", text)
	}
	// Tabs are copied through so the marker lands under the right glyph in a
	// terminal that renders them the same way.
	pad := make([]byte, 0, pos.Col-1)
	for i := 0; i < pos.Col-1; i++ {
		if text[i] == '\t' {
			pad = append(pad, '\t')
			continue
		}
		pad = append(pad, ' ')
	}
	return fmt.Sprintf("  %s\n  %s^", text, pad)
}

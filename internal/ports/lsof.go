package ports

import "strconv"

// cwdsFromLsof turns one `lsof -a -p <pids> -d cwd -Fpn` run into pid -> cwd.
// The field output is a record per line, `p<pid>` introducing a process and
// `n<path>` its working directory:
//
//	p1234
//	n/Users/me/project
//	p5678
//	n/var/empty
//
// The error is honoured only when lsof printed nothing at all — the same rule
// scanLsof already applies. lsof exits non-zero as soon as one pid it was
// asked about has gone away, which is routine when the caller is asking about
// every listener on the machine, and it still prints every record it did
// resolve. Dropping the whole batch on that error left *every* process without
// a cwd for that scan, and a process with no cwd has no git root, so its group
// and its display name silently fell back to something else for one tick and
// then changed back.
//
// It lives outside the darwin-only file so the parsing is tested everywhere,
// not only on the platform that runs lsof.
func cwdsFromLsof(out []byte, err error) map[int]string {
	result := make(map[int]string)
	if err != nil && len(out) == 0 {
		return result
	}

	current := 0
	start := 0
	text := string(out)
	for i := 0; i <= len(text); i++ {
		if i < len(text) && text[i] != '\n' {
			continue
		}
		line := text[start:i]
		start = i + 1
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, convErr := strconv.Atoi(line[1:])
			if convErr == nil && pid > 0 {
				current = pid
			} else {
				current = 0
			}
		case 'n':
			if current != 0 {
				result[current] = line[1:]
			}
		}
	}
	return result
}

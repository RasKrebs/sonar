package ports

// ParentTable returns a pid -> ppid map for every process this user can see.
// It is the same table the scanner builds while enriching listeners, exported
// so the daemon's runs registry can walk a listener's ancestry back to the
// `sonar start` that owns it without scanning ports first.
func ParentTable() map[int]int {
	info := batchGetPPIDsAndCommands()
	out := make(map[int]int, len(info))
	for pid, e := range info {
		out[pid] = e.ppid
	}
	return out
}

package sessions

// processTable has no cheap portable form on Windows: the CIM query the
// scanner uses is far too expensive for something that runs on every `sonar
// start`. Detection there relies on the agent's own session-id variable and
// otherwise falls back to the parent pid (see ancestorID).
func processTable() []Process { return nil }

package mcpserver

// SubscribedURIs is the set of resource URIs clients are watching right now.
// It exists for the tests, which need to know that a subscription has been
// registered: MCP's own subscribe is a long-lived request the client sends
// without waiting for it to be answered.
func (s *Server) SubscribedURIs() []string { return s.subs.uris() }

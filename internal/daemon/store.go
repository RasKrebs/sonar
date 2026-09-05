package daemon

import (
	"time"

	"github.com/raskrebs/sonar/internal/state"
	"github.com/raskrebs/sonar/internal/store"
)

// openStore opens the daemon's SQLite database and hands it to the runtime and
// the scanner. A database that cannot be opened at all is not fatal: the daemon
// keeps serving live state, only renames, pins and history are unavailable —
// which is strictly better than refusing to report ports because of a disk
// problem.
//
// A database that was unusable and had to be recreated raises
// state.event{kind: "db_reset"}, queued so the first subscriber sees it even
// though nobody is connected this early (daemon spec, "Error handling").
func (s *Server) openStore() {
	path := s.opts.DBPath
	if path == "" {
		path = store.Path()
	}
	st, err := store.Open(path)
	if err != nil {
		s.logger.Error("opening the database, continuing without renames, pins and history",
			"path", path, "error", err)
		return
	}
	s.setStore(st)
	s.logger.Info("database open", "path", st.DBPath())

	if reset := st.Reset(); reset != nil {
		s.logger.Warn("database was unusable and has been recreated",
			"path", reset.Path, "moved_to", reset.MovedTo, "reason", reset.Reason)
		s.queueEvent(state.Event{
			Kind: "db_reset",
			At:   time.Now().Format(time.RFC3339),
			Data: map[string]any{
				"path":      reset.Path,
				"moved_to":  reset.MovedTo,
				"reason":    reset.Reason,
				"recovered": true,
			},
		})
	}
}

// setStore installs an open store on the runtime and the scan loop. Tests use
// it to hand a harness a temp database.
func (s *Server) setStore(st *store.Store) {
	s.runtime.Store = st
	s.runtime.DB = st.DB()
	s.loop.SetStore(st)
}

// closeStore releases the database on shutdown.
func (s *Server) closeStore() {
	if s.runtime.Store == nil {
		return
	}
	if err := s.runtime.Store.Close(); err != nil {
		s.logger.Warn("closing the database", "error", err)
	}
	s.runtime.Store, s.runtime.DB = nil, nil
}

// queueEvent holds an event until the next publish carries it out. The daemon
// raises events before anyone can be listening — db_reset happens while the
// socket is still coming up — and dropping them would make the one condition a
// client most needs to know about invisible.
func (s *Server) queueEvent(ev state.Event) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.pending = append(s.pending, ev)
}

// takePending drains the queued events for the next publish.
func (s *Server) takePending() []state.Event {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	out := s.pending
	s.pending = nil
	return out
}

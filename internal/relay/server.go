package relay

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Options configures a Server. Everything has a working default except Store.
type Options struct {
	// Listen is the address to bind. Empty means ":8787". It binds all
	// interfaces on purpose: the relay's home is a container, where
	// 127.0.0.1 would make it unreachable. TLS is the proxy's job.
	Listen string
	// Store is the database. Required.
	Store Storage
	// ProjectKeys, when non-empty, turns on authentication: every request to
	// /v1/events and /v1/stats must present one of these keys.
	ProjectKeys []string
	// Retention is how long raw events are kept. Zero means 90 days. Rollups
	// are kept forever.
	Retention time.Duration
	// RollupEvery is the rollup/retention tick. Zero means one hour.
	RollupEvery time.Duration
	// Logger receives structured request and lifecycle logs.
	Logger *slog.Logger
	// Now is the clock, for tests. Zero means time.Now.
	Now func() time.Time
	// Rate and Burst tune the per-install token bucket. Zero means the
	// defaults; a negative Rate disables the limit.
	Rate  float64
	Burst int
}

// Server is the relay's HTTP service.
type Server struct {
	opts    Options
	store   Storage
	log     *slog.Logger
	now     func() time.Time
	limiter *limiter
	keys    []string
	mux     *http.ServeMux

	ln   net.Listener
	http *http.Server
}

// DefaultListen is where the relay binds when nothing says otherwise.
const DefaultListen = ":8787"

// DefaultRetention is how long raw events live by default.
const DefaultRetention = 90 * 24 * time.Hour

// New builds a Server. It does not listen; call Serve.
func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("relay: a Store is required")
	}
	if strings.TrimSpace(opts.Listen) == "" {
		opts.Listen = DefaultListen
	}
	if opts.Retention <= 0 {
		opts.Retention = DefaultRetention
	}
	if opts.RollupEvery <= 0 {
		opts.RollupEvery = time.Hour
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	rate, burst := opts.Rate, opts.Burst
	if rate == 0 {
		rate = DefaultRate
	}
	if burst <= 0 {
		burst = DefaultBurst
	}

	keys := make([]string, 0, len(opts.ProjectKeys))
	for _, k := range opts.ProjectKeys {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}

	s := &Server{
		opts:    opts,
		store:   opts.Store,
		log:     opts.Logger,
		now:     opts.Now,
		limiter: newLimiter(rate, burst, opts.Now),
		keys:    keys,
	}
	s.mux = http.NewServeMux()
	// Method-and-path patterns, so a GET to /v1/events is a 405 from the
	// router rather than a hand-written check in every handler.
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("POST /v1/events", s.handleEvents)
	s.mux.HandleFunc("GET /v1/stats", s.handleStats)
	return s, nil
}

// Handler is the router, for httptest and for embedding the relay in a larger
// service later.
func (s *Server) Handler() http.Handler { return s.logRequests(s.mux) }

// Addr is the address the server actually bound, "" before Serve has listened.
// A configured port of 0 makes this the only way to find the real one, which
// is what the integration test uses.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Listen binds the socket without serving, so a caller that needs the address
// (a test, or a supervisor that logs it) can have it before requests start.
func (s *Server) Listen() error {
	if s.ln != nil {
		return nil
	}
	ln, err := net.Listen("tcp", s.opts.Listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.opts.Listen, err)
	}
	s.ln = ln
	return nil
}

// Serve runs until ctx is cancelled, then shuts down gracefully: in-flight
// requests finish, new ones are refused, and the rollup loop stops. It returns
// nil on a clean shutdown.
func (s *Server) Serve(ctx context.Context) error {
	if err := s.Listen(); err != nil {
		return err
	}
	s.http = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// The relay's clients are one small POST at a time; a large header
		// is either a bug or an attempt.
		MaxHeaderBytes: 16 << 10,
	}

	maintenance := make(chan struct{})
	go func() {
		defer close(maintenance)
		s.maintain(ctx)
	}()

	s.log.Info("relay listening",
		"addr", s.Addr(),
		"auth", len(s.keys) > 0,
		"retention_days", int(s.opts.Retention/(24*time.Hour)))

	errc := make(chan error, 1)
	go func() {
		err := s.http.Serve(s.ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errc <- err
	}()

	select {
	case err := <-errc:
		<-maintenance
		return err
	case <-ctx.Done():
	}

	s.log.Info("relay shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := s.http.Shutdown(shutdownCtx)
	<-maintenance
	<-errc
	if err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	s.log.Info("relay stopped")
	return nil
}

// maintain runs the rollup and the retention delete on a tick, once at start
// so a container restart is also a repair.
func (s *Server) maintain(ctx context.Context) {
	tick := time.NewTicker(s.opts.RollupEvery)
	defer tick.Stop()
	for {
		s.maintainOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// MaintainOnce runs one rollup-then-retain pass. Exported for the integration
// test and for anything that wants the pass on demand; the order matters, and
// this is the only place it is written down.
func (s *Server) MaintainOnce(ctx context.Context) error {
	cells, err := s.store.Rollup(ctx)
	if err != nil {
		return err
	}
	deleted, err := s.store.Retain(ctx, s.now().Add(-s.opts.Retention))
	if err != nil {
		return err
	}
	s.log.Debug("relay maintenance", "rollup_cells", cells, "events_expired", deleted)
	return nil
}

func (s *Server) maintainOnce(ctx context.Context) {
	if err := s.MaintainOnce(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("relay maintenance failed", "err", err)
	}
}

// logRequests is the one place a request is logged, so every handler gets the
// same fields whether it succeeded or not.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.written,
			"ms", s.now().Sub(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	return n, err
}

// ---- handlers ----------------------------------------------------------

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	// Deliberately unauthenticated and deliberately cheap: it is what a
	// container orchestrator polls, and it must not depend on the database
	// being reachable to report that the process is alive.
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// authorized reports whether the request carries a configured project key.
// With no keys configured the relay is open, which is what a private
// deployment behind a VPN wants; the moment one key exists, every /v1 route
// needs one.
func (s *Server) authorized(r *http.Request) bool {
	if len(s.keys) == 0 {
		return true
	}
	given := strings.TrimSpace(r.Header.Get("X-Sonar-Key"))
	if given == "" {
		if v := strings.TrimSpace(r.Header.Get("Authorization")); v != "" {
			if rest, ok := cutPrefixFold(v, "Bearer "); ok {
				given = strings.TrimSpace(rest)
			}
		}
	}
	if given == "" {
		return false
	}
	ok := false
	// Every key is compared, in constant time, so neither the number of
	// configured keys nor the position of the match is timeable.
	for _, k := range s.keys {
		if subtle.ConstantTimeCompare([]byte(k), []byte(given)) == 1 {
			ok = true
		}
	}
	return ok
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, &Error{Code: "unauthorized",
			Reason: "this relay requires a project key in X-Sonar-Key or Authorization: Bearer",
			status: http.StatusUnauthorized})
		return
	}
	project, perr := NormalizeProject(r.Header.Get("X-Sonar-Project"))
	if perr != nil {
		writeError(w, perr)
		return
	}

	// The cap is enforced by the reader, not by Content-Length: a chunked
	// body has no length to trust.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, &Error{Code: "body_too_large",
				Reason: fmt.Sprintf("a batch body is at most %d bytes", MaxBodyBytes),
				status: http.StatusRequestEntityTooLarge})
			return
		}
		writeError(w, &Error{Code: "unreadable_body",
			Reason: "the request body could not be read", status: http.StatusBadRequest})
		return
	}

	var batch Batch
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&batch); err != nil {
		writeError(w, &Error{Code: "invalid_json",
			Reason: "the body is not a JSON object matching the events contract",
			status: http.StatusBadRequest})
		return
	}

	now := s.now()
	if verr := ValidateBatch(&batch, now); verr != nil {
		writeError(w, verr)
		return
	}

	// Rate limiting is keyed on install_id, so one noisy install cannot drown
	// out a whole NAT'd office. It runs after validation so an unparsable id
	// cannot claim someone else's bucket.
	if ok, wait := s.limiter.allow(batch.InstallID); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, &Error{Code: "rate_limited",
			Reason: "too many batches from this install; retry shortly",
			status: http.StatusTooManyRequests})
		return
	}

	if err := s.store.Record(r.Context(), project, batch, now); err != nil {
		s.log.Error("recording a batch failed", "err", err, "project", project)
		writeError(w, &Error{Code: "storage_error",
			Reason: "the batch could not be stored", status: http.StatusInternalServerError})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": len(batch.Events)})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, &Error{Code: "unauthorized",
			Reason: "this relay requires a project key in X-Sonar-Key or Authorization: Bearer",
			status: http.StatusUnauthorized})
		return
	}
	q := StatsQuery{Days: 30}
	if v := strings.TrimSpace(r.URL.Query().Get("days")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			writeError(w, badRequest("invalid_days", "days must be a whole number from 1 to 365"))
			return
		}
		q.Days = n
	}
	if v := strings.TrimSpace(r.URL.Query().Get("project")); v != "" {
		project, perr := NormalizeProject(v)
		if perr != nil {
			writeError(w, perr)
			return
		}
		q.Project = project
	}

	stats, err := s.store.Stats(r.Context(), q, s.now())
	if err != nil {
		s.log.Error("reading stats failed", "err", err)
		writeError(w, &Error{Code: "storage_error",
			Reason: "the stats could not be read", status: http.StatusInternalServerError})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, e *Error) {
	status := e.status
	if status == 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, e)
}

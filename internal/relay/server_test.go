package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, opts Options) (*Server, Storage) {
	t.Helper()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	opts.Store = store
	if opts.Now == nil {
		now := testNow
		opts.Now = func() time.Time { return now }
	}
	s, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, store
}

func post(t *testing.T, s *Server, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) Error {
	t.Helper()
	var e Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body %q is not JSON: %v", rec.Body.String(), err)
	}
	if e.Reason == "" {
		t.Fatalf("a %d must carry a reason, got %q", rec.Code, rec.Body.String())
	}
	return e
}

const okBody = `{"install_id":"8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c","app_version":"v0.6.0",` +
	`"os":"darwin","arch":"arm64","events":[{"name":"app_open","props":{"pane":"ports"}}]}`

func TestHealthzIsOpenAndCheap(t *testing.T) {
	s, _ := newTestServer(t, Options{ProjectKeys: []string{"secret"}})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200 even with keys configured", rec.Code)
	}
}

func TestPostEventsAccepts(t *testing.T) {
	s, store := newTestServer(t, Options{})
	rec := post(t, s, okBody, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /v1/events = %d (%s), want 202", rec.Code, rec.Body.String())
	}
	var body struct {
		Accepted int `json:"accepted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response %q: %v", rec.Body.String(), err)
	}
	if body.Accepted != 1 {
		t.Fatalf("accepted = %d, want 1", body.Accepted)
	}

	st, err := store.Stats(context.Background(), StatsQuery{}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if st.Installs.Total != 1 {
		t.Fatalf("the batch was accepted but no install was recorded")
	}
}

func TestPostEventsValidationStatuses(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		code   string
		status int
	}{
		{"not json", `not json`, "invalid_json", 400},
		{"bad install id", `{"install_id":"nope","events":[]}`, "invalid_install_id", 400},
		{"unknown name", `{"install_id":"8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c","events":[{"name":"nope"}]}`,
			"unknown_event", 400},
		{"path prop", `{"install_id":"8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c","events":` +
			`[{"name":"scan","props":{"root":"/Users/ada/code"}}]}`, "unsafe_prop", 400},
		{"nested prop", `{"install_id":"8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c","events":` +
			`[{"name":"scan","props":{"root":{"a":1}}}]}`, "invalid_prop", 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t, Options{})
			rec := post(t, s, tc.body, nil)
			if rec.Code != tc.status {
				t.Fatalf("status = %d (%s), want %d", rec.Code, rec.Body.String(), tc.status)
			}
			if got := decodeError(t, rec); got.Code != tc.code {
				t.Fatalf("error = %s, want %s", got.Code, tc.code)
			}
		})
	}
}

func TestPostEventsRejectsAnOversizedBody(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	// Valid JSON, just far too much of it: the cap has to bite before the
	// parser does, so the relay never allocates what a client claims to send.
	big := `{"install_id":"8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c","pad":"` +
		strings.Repeat("x", MaxBodyBytes+1) + `","events":[]}`
	rec := post(t, s, big, nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d (%s), want 413", rec.Code, rec.Body.String())
	}
	if got := decodeError(t, rec); got.Code != "body_too_large" {
		t.Fatalf("error = %s, want body_too_large", got.Code)
	}
}

func TestPostEventsUnknownFieldsAreIgnored(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	body := `{"install_id":"8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c","schema":"v2",` +
		`"events":[{"name":"app_open","severity":"low"}]}`
	if rec := post(t, s, body, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("a newer client was rejected: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPostEventsAcceptsSentAt(t *testing.T) {
	// `sent_at` is in the milestone 5 contract's body but means nothing to the
	// relay, which stamps its own received_at. It has to be accepted and
	// ignored rather than rejected as an unknown field — that is the whole
	// forward-compatibility rule, pinned on the one field known to exercise it.
	s, store := newTestServer(t, Options{})
	body := `{"install_id":"8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c","app_version":"v0.6.0",` +
		`"os":"darwin","arch":"arm64","sent_at":"2026-09-06T12:00:04Z",` +
		`"events":[{"name":"view","props":{"name":"ports"}}]}`
	rec := post(t, s, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a batch with sent_at = %d (%s), want 202", rec.Code, rec.Body.String())
	}
	st, err := store.Stats(context.Background(), StatsQuery{}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Events) != 1 || st.Events[0].Name != "view" {
		t.Fatalf("events = %+v, want the view event stored", st.Events)
	}
}

func TestProjectHeader(t *testing.T) {
	s, store := newTestServer(t, Options{})
	if rec := post(t, s, okBody, map[string]string{"X-Sonar-Project": "acme-eu"}); rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	st, err := store.Stats(context.Background(), StatsQuery{Project: "acme-eu"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if st.Installs.Total != 1 {
		t.Fatalf("the batch did not land in project acme-eu")
	}

	rec := post(t, s, okBody, map[string]string{"X-Sonar-Project": "Acme EU"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a malformed project header = %d, want 400", rec.Code)
	}
	if got := decodeError(t, rec); got.Code != "invalid_project" {
		t.Fatalf("error = %s, want invalid_project", got.Code)
	}
}

func TestAuth(t *testing.T) {
	s, _ := newTestServer(t, Options{ProjectKeys: []string{"key-one", "key-two"}})

	t.Run("no key", func(t *testing.T) {
		rec := post(t, s, okBody, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if got := decodeError(t, rec); got.Code != "unauthorized" {
			t.Fatalf("error = %s", got.Code)
		}
	})
	t.Run("wrong key", func(t *testing.T) {
		if rec := post(t, s, okBody, map[string]string{"X-Sonar-Key": "nope"}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
	t.Run("x-sonar-key", func(t *testing.T) {
		if rec := post(t, s, okBody, map[string]string{"X-Sonar-Key": "key-two"}); rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d (%s), want 202", rec.Code, rec.Body.String())
		}
	})
	t.Run("bearer", func(t *testing.T) {
		if rec := post(t, s, okBody, map[string]string{"Authorization": "Bearer key-one"}); rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d (%s), want 202", rec.Code, rec.Body.String())
		}
	})
	t.Run("stats needs a key too", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET /v1/stats without a key = %d, want 401", rec.Code)
		}
	})
}

func TestNoKeysMeansOpen(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	if rec := post(t, s, okBody, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("an unauthenticated relay refused a batch: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRateLimit(t *testing.T) {
	now := testNow
	s, _ := newTestServer(t, Options{
		Rate:  1,
		Burst: 3,
		Now:   func() time.Time { return now },
	})

	for i := 0; i < 3; i++ {
		if rec := post(t, s, okBody, nil); rec.Code != http.StatusAccepted {
			t.Fatalf("request %d = %d (%s), the burst should cover it", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := post(t, s, okBody, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the fourth request = %d, want 429", rec.Code)
	}
	if got := decodeError(t, rec); got.Code != "rate_limited" {
		t.Fatalf("error = %s, want rate_limited", got.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a 429 must say when to come back")
	}

	// A different install has its own bucket.
	other := strings.Replace(okBody, "8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c",
		"11111111-2222-4333-8444-555555555555", 1)
	if rec := post(t, s, other, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("one noisy install throttled another: %d", rec.Code)
	}

	// Tokens come back with the clock.
	now = now.Add(5 * time.Second)
	if rec := post(t, s, okBody, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("the bucket did not refill: %d", rec.Code)
	}
}

func TestRateLimitRunsAfterValidation(t *testing.T) {
	// A body that cannot name an install must not be able to spend anyone's
	// tokens, so a rejected batch never touches the limiter.
	now := testNow
	s, _ := newTestServer(t, Options{Rate: 1, Burst: 1, Now: func() time.Time { return now }})
	for i := 0; i < 5; i++ {
		if rec := post(t, s, `{"install_id":"nope","events":[]}`, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	}
	if rec := post(t, s, okBody, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("junk requests drained a real install's bucket: %d", rec.Code)
	}
}

func TestStatsEndpoint(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	if rec := post(t, s, okBody, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("seeding failed: %s", rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/stats?days=7", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/stats = %d (%s)", rec.Code, rec.Body.String())
	}
	var st Stats
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("stats body %q: %v", rec.Body.String(), err)
	}
	if st.Days != 7 {
		t.Errorf("days = %d, want 7", st.Days)
	}
	if st.Installs.Active1d != 1 || st.Installs.Total != 1 {
		t.Errorf("installs = %+v, want one active install", st.Installs)
	}
	if len(st.Events) != 1 || st.Events[0].Name != "app_open" || st.Events[0].Count != 1 {
		t.Errorf("events = %+v, want one app_open", st.Events)
	}
	if st.GeneratedAt == "" {
		t.Error("stats must say when they were generated")
	}
}

func TestStatsRejectsABadWindow(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	for _, q := range []string{"days=0", "days=999", "days=lots"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/stats?"+q, nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("?%s = %d, want 400", q, rec.Code)
		}
	}
}

func TestMethodsAreRouted(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/events"},
		{http.MethodPost, "/v1/stats"},
		{http.MethodGet, "/nope"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusOK || rec.Code == http.StatusAccepted {
			t.Errorf("%s %s = %d, want a refusal", tc.method, tc.path, rec.Code)
		}
	}
}

func TestMaintainOnceRollsUpAndExpires(t *testing.T) {
	now := testNow
	s, store := newTestServer(t, Options{
		Retention: 24 * time.Hour,
		Now:       func() time.Time { return now },
	})
	old := now.Add(-72 * time.Hour)
	if err := store.Record(context.Background(), "default",
		batchAt(idN(1), old, "app_open"), old); err != nil {
		t.Fatal(err)
	}
	if err := s.MaintainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := store.Stats(context.Background(), StatsQuery{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := cell(st.Events, old.UTC().Format("2006-01-02"), "app_open"); got.Count != 1 {
		t.Fatalf("maintenance dropped the event instead of rolling it up first: %+v", got)
	}
	ss := store.(*sqlStore)
	var raw int
	if err := ss.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 0 {
		t.Fatalf("%d raw events survived retention", raw)
	}
}

func TestServeShutsDownGracefully(t *testing.T) {
	s, _ := newTestServer(t, Options{Listen: "127.0.0.1:0", RollupEvery: time.Hour})
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	addr := s.Addr()
	if addr == "" {
		t.Fatal("Addr is empty after Listen")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	var resp *http.Response
	var err error
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the server never came up: %v", err)
	}
	_ = resp.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v, want a clean shutdown", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Serve did not return after the context was cancelled")
	}
}

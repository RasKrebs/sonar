package store

import (
	"fmt"
	"testing"
	"time"
)

func TestAppendAndQuery(t *testing.T) {
	s := openTemp(t)
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	events := []HistoryEvent{
		{At: base, Kind: EventPortUp, Port: 3000, PID: 100, DisplayName: "api", Group: "shop"},
		{At: base.Add(time.Minute), Kind: EventPortUp, Port: 5173, PID: 101, DisplayName: "web", Group: "shop"},
		{At: base.Add(2 * time.Minute), Kind: EventPortRestarted, Port: 3000, PID: 102, DisplayName: "api", Group: "shop"},
		{At: base.Add(3 * time.Minute), Kind: EventPortDown, Port: 5173, PID: 101, DisplayName: "web", Group: "shop"},
	}
	for _, e := range events {
		if err := s.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := s.Query(nil, time.Time{}, 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("Query returned %d events, want 4", len(got))
	}
	// Newest first.
	if got[0].Kind != EventPortDown || got[3].Kind != EventPortUp || got[3].Port != 3000 {
		t.Errorf("Query is not newest-first: %+v", got)
	}
	if !got[0].At.Equal(base.Add(3 * time.Minute)) {
		t.Errorf("At round-tripped as %v, want %v", got[0].At, base.Add(3*time.Minute))
	}
	if got[0].DisplayName != "web" || got[0].Group != "shop" || got[0].PID != 101 {
		t.Errorf("row fields round-tripped wrong: %+v", got[0])
	}

	// Port filter.
	p := 3000
	only, err := s.Query(&p, time.Time{}, 0)
	if err != nil {
		t.Fatalf("Query(port): %v", err)
	}
	if len(only) != 2 {
		t.Fatalf("Query(3000) returned %d events, want 2", len(only))
	}
	for _, e := range only {
		if e.Port != 3000 {
			t.Errorf("port filter leaked port %d", e.Port)
		}
	}

	// since filter.
	recent, err := s.Query(nil, base.Add(2*time.Minute), 0)
	if err != nil {
		t.Fatalf("Query(since): %v", err)
	}
	if len(recent) != 2 {
		t.Errorf("Query(since=+2m) returned %d events, want 2", len(recent))
	}

	// limit.
	one, err := s.Query(nil, time.Time{}, 1)
	if err != nil {
		t.Fatalf("Query(limit): %v", err)
	}
	if len(one) != 1 || one[0].Kind != EventPortDown {
		t.Errorf("Query(limit=1) = %+v, want the newest event only", one)
	}
}

func TestAppendDefaults(t *testing.T) {
	s := openTemp(t)
	before := time.Now().Add(-time.Second)

	if err := s.Append(HistoryEvent{Kind: EventPortUp, Port: 3000}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := s.Query(nil, time.Time{}, 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].At.Before(before) {
		t.Errorf("zero At was not filled in with now: %v", got[0].At)
	}
	if err := s.Append(HistoryEvent{Port: 3000}); err == nil {
		t.Error("Append accepted an event with no kind")
	}
}

func TestQueryDefaultLimit(t *testing.T) {
	s := openTemp(t)
	batch := make([]HistoryEvent, 0, DefaultHistoryLimit+10)
	for i := range cap(batch) {
		batch = append(batch, HistoryEvent{Kind: EventPortUp, Port: 3000 + i})
	}
	if err := s.AppendBatch(batch); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	got, err := s.Query(nil, time.Time{}, 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != DefaultHistoryLimit {
		t.Errorf("Query with no limit returned %d events, want %d", len(got), DefaultHistoryLimit)
	}
}

// The ring trigger keeps exactly the newest 10 000 rows.
func TestHistoryRingKeepsTenThousandRows(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 10 500 rows")
	}
	s := openTemp(t)
	const overflow = 500

	batch := make([]HistoryEvent, 0, HistoryCapacity+overflow)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range HistoryCapacity + overflow {
		batch = append(batch, HistoryEvent{
			At:          base.Add(time.Duration(i) * time.Second),
			Kind:        EventPortUp,
			Port:        3000,
			PID:         i,
			DisplayName: fmt.Sprintf("event-%d", i),
		})
	}
	if err := s.AppendBatch(batch); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	n, err := s.HistoryCount()
	if err != nil {
		t.Fatalf("HistoryCount: %v", err)
	}
	if n != HistoryCapacity {
		t.Fatalf("ring holds %d rows, want exactly %d", n, HistoryCapacity)
	}

	newest, err := s.Query(nil, time.Time{}, 1)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if newest[0].DisplayName != fmt.Sprintf("event-%d", HistoryCapacity+overflow-1) {
		t.Errorf("newest row = %q, want the last one written", newest[0].DisplayName)
	}

	// The oldest `overflow` rows were dropped; the row right after them stayed.
	oldest, err := s.Query(nil, time.Time{}, HistoryCapacity)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if want := fmt.Sprintf("event-%d", overflow); oldest[len(oldest)-1].DisplayName != want {
		t.Errorf("oldest surviving row = %q, want %q", oldest[len(oldest)-1].DisplayName, want)
	}

	// One more insert evicts one more row and the count stays put.
	if err := s.Append(HistoryEvent{Kind: EventPortDown, Port: 3000, DisplayName: "last"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if n, err = s.HistoryCount(); err != nil || n != HistoryCapacity {
		t.Errorf("after one more insert the ring holds %d rows (%v), want %d", n, err, HistoryCapacity)
	}
}

func TestAppendBatchIsAtomic(t *testing.T) {
	s := openTemp(t)
	err := s.AppendBatch([]HistoryEvent{
		{Kind: EventPortUp, Port: 3000},
		{Port: 3001}, // no kind: the whole batch must roll back
	})
	if err == nil {
		t.Fatal("AppendBatch accepted an event with no kind")
	}
	if n, err := s.HistoryCount(); err != nil || n != 0 {
		t.Errorf("failed batch left %d rows (%v), want 0", n, err)
	}
	if err := s.AppendBatch(nil); err != nil {
		t.Errorf("AppendBatch(nil) = %v, want nil", err)
	}
}

func TestHistorySurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sonar.db"

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := first.Append(HistoryEvent{Kind: EventPortUp, Port: 3000, DisplayName: "api"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()

	got, err := second.Query(nil, time.Time{}, 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].DisplayName != "api" {
		t.Errorf("history after reopen = %+v, want the api event", got)
	}
}

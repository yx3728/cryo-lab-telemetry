// Package metricsx tracks honest, in-process telemetry for the /metrics
// endpoint: process uptime, rows ingested since start, and the last time each
// source successfully delivered data. These counters reset on restart (and say
// so), which is exactly why /metrics also reports DB-backed totals — we never
// invent numbers.
package metricsx

import (
	"sync"
	"time"
)

// Tracker is safe for concurrent use by the ingest handler's goroutines.
type Tracker struct {
	start time.Time

	mu        sync.Mutex
	total     int64
	perSource map[string]*sourceStat
	now       func() time.Time // injectable clock for tests
}

type sourceStat struct {
	rows         int64
	lastReceived time.Time
}

// New returns a Tracker started "now".
func New() *Tracker { return newWithClock(time.Now) }

func newWithClock(now func() time.Time) *Tracker {
	return &Tracker{
		start:     now(),
		perSource: make(map[string]*sourceStat),
		now:       now,
	}
}

// RecordIngest accounts for n rows just accepted from source.
func (t *Tracker) RecordIngest(source string, n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.total += n
	st := t.perSource[source]
	if st == nil {
		st = &sourceStat{}
		t.perSource[source] = st
	}
	st.rows += n
	st.lastReceived = t.now()
}

// SourceSnapshot is the per-source view exposed by /metrics.
type SourceSnapshot struct {
	RowsSinceStart int64      `json:"rows_since_start"`
	LastReceived   *time.Time `json:"last_received"`
}

// Snapshot returns a consistent copy of the current counters.
func (t *Tracker) Snapshot() (uptime time.Duration, total int64, sources map[string]SourceSnapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sources = make(map[string]SourceSnapshot, len(t.perSource))
	for src, st := range t.perSource {
		s := SourceSnapshot{RowsSinceStart: st.rows}
		if !st.lastReceived.IsZero() {
			lr := st.lastReceived
			s.LastReceived = &lr
		}
		sources[src] = s
	}
	return t.now().Sub(t.start), t.total, sources
}

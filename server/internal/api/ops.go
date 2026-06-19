package api

import (
	"net/http"
	"time"
)

// handleHealth is a liveness + DB-connectivity probe.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "degraded", "db": false,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "db": true})
}

// sourceMetrics is the per-source telemetry. We expose two distinct "last seen"
// notions and label them honestly:
//   - last_received: wall-clock time THIS process last accepted data (resets on
//     restart);
//   - last_reading_ts: the newest reading timestamp in the database (survives
//     restarts, authoritative).
type sourceMetrics struct {
	RowsSinceStart int64      `json:"rows_since_start"`
	LastReceived   *time.Time `json:"last_received"`
	LastReadingTS  *time.Time `json:"last_reading_ts"`
}

type metricsResponse struct {
	UptimeSeconds          int64                    `json:"uptime_seconds"`
	StartedAt              string                   `json:"started_at"`
	RowsIngestedSinceStart int64                    `json:"rows_ingested_since_start"`
	TotalRowsInDB          int64                    `json:"total_rows_in_db"`
	Sources                map[string]sourceMetrics `json:"sources"`
	Note                   string                   `json:"note"`
}

// handleMetrics reports honest telemetry: process uptime, rows ingested since
// start (in-memory counters), and DB-backed totals / last-seen. Counters that
// reset on restart are labelled as such; nothing here is fabricated.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	uptime, total, inMem := s.metrics.Snapshot()

	resp := metricsResponse{
		UptimeSeconds:          int64(uptime.Seconds()),
		StartedAt:              s.startedAt.UTC().Format(time.RFC3339),
		RowsIngestedSinceStart: total,
		Sources:                make(map[string]sourceMetrics),
		Note:                   "rows_ingested_since_start and last_received reset on restart; total_rows_in_db and last_reading_ts are DB-backed",
	}

	// Seed from in-memory per-source counters.
	for src, st := range inMem {
		resp.Sources[src] = sourceMetrics{RowsSinceStart: st.RowsSinceStart, LastReceived: st.LastReceived}
	}

	// Enrich with DB-backed totals and last-reading timestamps.
	if n, err := s.store.TotalRows(r.Context()); err == nil {
		resp.TotalRowsInDB = n
	}
	if lastSeen, err := s.store.SourceLastSeen(r.Context()); err == nil {
		for src, ts := range lastSeen {
			ts := ts
			m := resp.Sources[src] // zero value if source seen in DB but not this process
			m.LastReadingTS = &ts
			resp.Sources[src] = m
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

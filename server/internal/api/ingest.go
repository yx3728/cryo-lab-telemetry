package api

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/yx3728/lab-monitor/server/internal/store"
)

// maxIngestBytes caps the request body to protect the server from oversized
// payloads. Our batches are tiny (a handful of channels every few seconds); 4 MiB
// is generous headroom even for a backlog flush from the offline buffer.
const maxIngestBytes = 4 << 20

// maxBatchReadings bounds how many readings one request may carry.
const maxBatchReadings = 50_000

// ingestReading is one sample in an ingest batch. ts is parsed as RFC3339.
type ingestReading struct {
	Source string    `json:"source"`
	Metric string    `json:"metric"`
	TS     time.Time `json:"ts"`
	Value  float64   `json:"value"`
}

// ingestResponse reports what happened. received is what we parsed; inserted is
// what was actually written (received minus idempotent duplicates).
type ingestResponse struct {
	Received int   `json:"received"`
	Inserted int64 `json:"inserted"`
}

// handleIngest accepts a batch of readings from an authenticated source and
// writes them idempotently. The source is taken from the API token, not trusted
// from the body: a token bound to "unisoku-stm" can only ever write that source.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	source, ok := sourceFromContext(r.Context())
	if !ok { // should never happen: middleware guarantees it
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxIngestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var batch []ingestReading
	if err := dec.Decode(&batch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if len(batch) == 0 {
		writeJSON(w, http.StatusOK, ingestResponse{Received: 0, Inserted: 0})
		return
	}
	if len(batch) > maxBatchReadings {
		writeError(w, http.StatusRequestEntityTooLarge, "batch too large")
		return
	}

	readings := make([]store.Reading, 0, len(batch))
	for i, b := range batch {
		if err := validateReading(b, source); err != "" {
			writeError(w, http.StatusBadRequest, "reading "+strconv.Itoa(i)+": "+err)
			return
		}
		readings = append(readings, store.Reading{
			Source: source, // authoritative: from the token
			Metric: b.Metric,
			TS:     b.TS,
			Value:  b.Value,
		})
	}

	inserted, err := s.store.InsertReadings(r.Context(), readings)
	if err != nil {
		s.log.Error("ingest: insert failed", "err", err, "source", source)
		writeError(w, http.StatusInternalServerError, "failed to store readings")
		return
	}

	// Update telemetry (always, so last-seen advances even on an all-duplicate
	// retry) and evaluate alert thresholds against the accepted readings.
	s.metrics.RecordIngest(source, inserted)
	s.alerter.Check(r.Context(), readings)

	writeJSON(w, http.StatusOK, ingestResponse{Received: len(batch), Inserted: inserted})
}

// validateReading returns an empty string if valid, else a reason. The body's
// source, if present, must match the token's source (defence in depth).
func validateReading(b ingestReading, tokenSource string) string {
	if b.Metric == "" {
		return "metric is required"
	}
	if b.Source != "" && b.Source != tokenSource {
		return "source does not match API token"
	}
	if b.TS.IsZero() {
		return "ts is required (RFC3339)"
	}
	if math.IsNaN(b.Value) || math.IsInf(b.Value, 0) {
		return "value must be a finite number"
	}
	return ""
}

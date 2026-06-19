package api

import (
	"encoding/csv"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/yx3728/lab-monitor/server/internal/store"
)

// defaultRange is used when the caller omits from/to.
const defaultRange = 3 * time.Hour

// stepPattern restricts the bucket width to a simple "<n><unit>" form. The value
// is also passed as a bound parameter ($1::interval), so this is belt-and-braces
// against a malformed interval, not the only line of defence.
var stepPattern = regexp.MustCompile(`^[0-9]{1,4}(s|m|h|d)$`)

// seriesResponse is the downsampled payload the dashboard charts.
type seriesResponse struct {
	Source string         `json:"source"`
	Metric string         `json:"metric"`
	Step   string         `json:"step"`
	From   time.Time      `json:"from"`
	To     time.Time      `json:"to"`
	Points []store.Bucket `json:"points"`
}

// handleSeries returns a channel downsampled into time buckets. Public read.
func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	metric := r.URL.Query().Get("metric")
	if source == "" || metric == "" {
		writeError(w, http.StatusBadRequest, "source and metric are required")
		return
	}
	from, to, err := parseTimeRange(r)
	if err != "" {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	step := resolveStep(r.URL.Query().Get("step"), from, to)

	buckets, qErr := s.store.QuerySeries(r.Context(), source, metric, from, to, step)
	if qErr != nil {
		s.log.Error("series query failed", "err", qErr)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	if buckets == nil {
		buckets = []store.Bucket{} // serialise as [] not null
	}
	writeJSON(w, http.StatusOK, seriesResponse{
		Source: source, Metric: metric, Step: step, From: from, To: to, Points: buckets,
	})
}

// handleExportCSV streams the same downsampled series as CSV. Public read.
func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	metric := r.URL.Query().Get("metric")
	if source == "" || metric == "" {
		writeError(w, http.StatusBadRequest, "source and metric are required")
		return
	}
	from, to, errMsg := parseTimeRange(r)
	if errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	step := resolveStep(r.URL.Query().Get("step"), from, to)

	buckets, err := s.store.QuerySeries(r.Context(), source, metric, from, to, step)
	if err != nil {
		s.log.Error("export query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition",
		"attachment; filename=\""+source+"_"+metric+".csv\"")

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"ts", "source", "metric", "value"})
	for _, b := range buckets {
		_ = cw.Write([]string{
			b.TS.UTC().Format(time.RFC3339),
			source, metric,
			strconv.FormatFloat(b.Value, 'g', -1, 64),
		})
	}
	cw.Flush()
}

// parseTimeRange reads from/to (RFC3339), defaulting to the last defaultRange.
// Returns ("") error string when valid.
func parseTimeRange(r *http.Request) (time.Time, time.Time, string) {
	q := r.URL.Query()
	to := time.Now()
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, "invalid 'to' (want RFC3339)"
		}
		to = t
	}
	from := to.Add(-defaultRange)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, "invalid 'from' (want RFC3339)"
		}
		from = t
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, "'from' must be before 'to'"
	}
	return from, to, ""
}

// resolveStep returns the caller's step if valid, otherwise an automatic bucket
// width chosen to keep charts readable (~hundreds of points across the range).
func resolveStep(requested string, from, to time.Time) string {
	if stepPattern.MatchString(requested) {
		return requested
	}
	span := to.Sub(from)
	switch {
	case span <= time.Hour:
		return "5s"
	case span <= 6*time.Hour:
		return "30s"
	case span <= 24*time.Hour:
		return "2m"
	case span <= 7*24*time.Hour:
		return "15m"
	default:
		return "1h"
	}
}

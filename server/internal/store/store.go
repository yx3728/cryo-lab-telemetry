// Package store is the only place that talks to Postgres/TimescaleDB. It uses
// pgx directly (no ORM) so every query is explicit and reviewable. Handlers call
// these methods; the database is the system's single source of truth.
package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps a connection pool. It is safe for concurrent use: pgxpool hands a
// connection to each goroutine, which is exactly the concurrency model the
// ingest path relies on.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a pgx connection pool to databaseURL and verifies connectivity.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping checks database liveness (used by /healthz).
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Reading is one sample of one channel.
type Reading struct {
	Source string    `json:"source"`
	Metric string    `json:"metric"`
	TS     time.Time `json:"ts"`
	Value  float64   `json:"value"`
}

// maxParamsPerStmt keeps a multi-row INSERT under Postgres' 65535-parameter cap
// (4 params per row). 1000 rows/statement is comfortably below it and large
// enough that batching overhead is negligible for our few-second cadence.
const maxRowsPerStmt = 1000

// InsertReadings writes a batch idempotently and returns how many rows were
// actually inserted (duplicates are silently skipped via ON CONFLICT DO
// NOTHING). Because the unique key is (source, metric, ts), a retried or
// replayed batch never double-writes — this is the server half of "reliable
// delivery".
func (s *Store) InsertReadings(ctx context.Context, readings []Reading) (int64, error) {
	var inserted int64
	for start := 0; start < len(readings); start += maxRowsPerStmt {
		end := start + maxRowsPerStmt
		if end > len(readings) {
			end = len(readings)
		}
		n, err := s.insertChunk(ctx, readings[start:end])
		if err != nil {
			return inserted, err
		}
		inserted += n
	}
	return inserted, nil
}

func (s *Store) insertChunk(ctx context.Context, chunk []Reading) (int64, error) {
	var b strings.Builder
	b.WriteString("INSERT INTO readings (source, metric, ts, value) VALUES ")
	args := make([]any, 0, len(chunk)*4)
	for i, r := range chunk {
		if i > 0 {
			b.WriteByte(',')
		}
		n := i * 4
		// $1,$2,$3,$4 then $5,$6,$7,$8 ...
		b.WriteString("($" + strconv.Itoa(n+1) + ",$" + strconv.Itoa(n+2) +
			",$" + strconv.Itoa(n+3) + ",$" + strconv.Itoa(n+4) + ")")
		args = append(args, r.Source, r.Metric, r.TS, r.Value)
	}
	b.WriteString(" ON CONFLICT (source, metric, ts) DO NOTHING")

	tag, err := s.pool.Exec(ctx, b.String(), args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Bucket is one downsampled point returned by the read API.
type Bucket struct {
	TS    time.Time `json:"ts"`
	Value float64   `json:"value"`
}

// QuerySeries returns the channel downsampled into `step`-wide time buckets
// (TimescaleDB's time_bucket), averaging the readings in each bucket. step is a
// Postgres interval string such as "5s" or "1m".
func (s *Store) QuerySeries(ctx context.Context, source, metric string, from, to time.Time, step string) ([]Bucket, error) {
	const q = `
		SELECT time_bucket($1::interval, ts) AS bucket, avg(value) AS value
		FROM readings
		WHERE source = $2 AND metric = $3 AND ts >= $4 AND ts < $5
		GROUP BY bucket
		ORDER BY bucket`
	rows, err := s.pool.Query(ctx, q, step, source, metric, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Bucket
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.TS, &b.Value); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Channel is the latest known state of one (source, metric) pair, used by the
// dashboard to discover which channels actually have data.
type Channel struct {
	Source    string    `json:"source"`
	Metric    string    `json:"metric"`
	LastTS    time.Time `json:"last_ts"`
	LastValue float64   `json:"last_value"`
}

// Channels returns the most recent reading per (source, metric), ordered for
// stable display.
func (s *Store) Channels(ctx context.Context) ([]Channel, error) {
	const q = `
		SELECT DISTINCT ON (source, metric) source, metric, ts, value
		FROM readings
		ORDER BY source, metric, ts DESC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.Source, &c.Metric, &c.LastTS, &c.LastValue); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- config (control plane key/value) ---------------------------------------

// GetConfigValue reads a single config key.
func (s *Store) GetConfigValue(ctx context.Context, key string) (string, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT value FROM config WHERE key = $1`, key).Scan(&v)
	return v, err
}

// SetConfigValue upserts a single config key.
func (s *Store) SetConfigValue(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO config (key, value, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		key, value)
	return err
}

// --- thresholds --------------------------------------------------------------

// Threshold is an alert rule for one metric. Min/Max are nullable: a rule may
// bound one side only.
type Threshold struct {
	Metric  string   `json:"metric"`
	Min     *float64 `json:"min"`
	Max     *float64 `json:"max"`
	Enabled bool     `json:"enabled"`
}

// GetThresholds returns all configured thresholds, ordered by metric.
func (s *Store) GetThresholds(ctx context.Context) ([]Threshold, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT metric, min_value, max_value, enabled FROM alert_threshold ORDER BY metric`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Threshold
	for rows.Next() {
		var t Threshold
		if err := rows.Scan(&t.Metric, &t.Min, &t.Max, &t.Enabled); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpsertThreshold inserts or updates one metric's threshold rule.
func (s *Store) UpsertThreshold(ctx context.Context, t Threshold) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO alert_threshold (metric, min_value, max_value, enabled)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (metric) DO UPDATE
		  SET min_value = EXCLUDED.min_value,
		      max_value = EXCLUDED.max_value,
		      enabled   = EXCLUDED.enabled`,
		t.Metric, t.Min, t.Max, t.Enabled)
	return err
}

// --- alert log ---------------------------------------------------------------

// AlertEvent records one threshold cross.
type AlertEvent struct {
	Source         string
	Metric         string
	Value          float64
	Kind           string // "min" or "max"
	ThresholdValue float64
	Notified       bool
}

// InsertAlertLog appends a fired alert to the audit trail.
func (s *Store) InsertAlertLog(ctx context.Context, e AlertEvent) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO alert_log (source, metric, value, kind, threshold_value, notified)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		e.Source, e.Metric, e.Value, e.Kind, e.ThresholdValue, e.Notified)
	return err
}

// LastAlertTime returns the most recent fired_at for a metric, used to debounce
// repeated alerts across restarts (the alert log is the debounce state).
func (s *Store) LastAlertTime(ctx context.Context, metric string) (time.Time, bool, error) {
	var t time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT fired_at FROM alert_log WHERE metric = $1 ORDER BY fired_at DESC LIMIT 1`,
		metric).Scan(&t)
	if err != nil {
		if isNoRows(err) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	return t, true, nil
}

// --- telemetry (/metrics) ----------------------------------------------------

// SourceLastSeen returns the latest reading timestamp per source, straight from
// the data — an honest "when did we last hear from each instrument".
func (s *Store) SourceLastSeen(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.pool.Query(ctx, `SELECT source, max(ts) FROM readings GROUP BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]time.Time)
	for rows.Next() {
		var src string
		var ts time.Time
		if err := rows.Scan(&src, &ts); err != nil {
			return nil, err
		}
		out[src] = ts
	}
	return out, rows.Err()
}

// TotalRows returns the total number of stored readings.
func (s *Store) TotalRows(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM readings`).Scan(&n)
	return n, err
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

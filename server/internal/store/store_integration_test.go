package store

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

// These tests exercise the real SQL against a live TimescaleDB. They are skipped
// unless TEST_DATABASE_URL is set, so `go test ./...` stays fast and hermetic by
// default:
//
//	TEST_DATABASE_URL=postgres://labmon:labmon-dev-password@localhost:5432/labmon?sslmode=disable \
//	  go test ./internal/store -run Integration -v
func newTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run store integration tests")
	}
	ctx := context.Background()
	s, err := New(ctx, url, 10)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s, ctx
}

// uniqueSource isolates each test run's data so reruns don't collide.
func uniqueSource(prefix string) string {
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func TestIntegration_IngestIdempotency(t *testing.T) {
	s, ctx := newTestStore(t)
	source := uniqueSource("itest-idem")
	base := time.Date(2026, 6, 18, 22, 0, 0, 0, time.UTC)

	batch := []Reading{
		{Source: source, Metric: "OC", TS: base, Value: 2.3e-9},
		{Source: source, Metric: "OC", TS: base.Add(5 * time.Second), Value: 2.4e-9},
		{Source: source, Metric: "STM", TS: base, Value: 4.2},
	}

	n, err := s.InsertReadings(ctx, batch)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if n != 3 {
		t.Fatalf("first insert wrote %d rows, want 3", n)
	}

	// Replaying the identical batch must write nothing (idempotent).
	n, err = s.InsertReadings(ctx, batch)
	if err != nil {
		t.Fatalf("re-insert: %v", err)
	}
	if n != 0 {
		t.Fatalf("replay wrote %d rows, want 0 (idempotency broken)", n)
	}

	// A partially-overlapping batch writes only the new row.
	n, err = s.InsertReadings(ctx, append(batch, Reading{
		Source: source, Metric: "STM", TS: base.Add(5 * time.Second), Value: 25.5,
	}))
	if err != nil {
		t.Fatalf("overlap insert: %v", err)
	}
	if n != 1 {
		t.Fatalf("overlap insert wrote %d rows, want 1", n)
	}
}

func TestIntegration_QuerySeries(t *testing.T) {
	s, ctx := newTestStore(t)
	source := uniqueSource("itest-series")
	base := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	var batch []Reading
	for i := 0; i < 12; i++ {
		batch = append(batch, Reading{
			Source: source, Metric: "OC", TS: base.Add(time.Duration(i) * 5 * time.Second), Value: float64(i),
		})
	}
	if _, err := s.InsertReadings(ctx, batch); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// A 30s step routes to readings_1s; materialize it for this (old, isolated)
	// range so the test is deterministic. On the live box the refresh policy +
	// real-time aggregation keep recent data fresh without a manual refresh.
	if _, err := s.pool.Exec(ctx,
		"CALL refresh_continuous_aggregate('readings_1s', $1::timestamptz, $2::timestamptz)",
		base.Add(-time.Hour), base.Add(time.Hour)); err != nil {
		t.Fatalf("refresh 1s cagg: %v", err)
	}

	// 30s buckets over a 60s span -> 2 buckets, each averaging 6 readings.
	buckets, err := s.QuerySeries(ctx, source, "OC", base, base.Add(60*time.Second), "30s")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2", len(buckets))
	}
	if buckets[0].Value != 2.5 { // avg of 0..5
		t.Fatalf("bucket0 avg = %v, want 2.5", buckets[0].Value)
	}
}

func TestIntegration_RollupSeries(t *testing.T) {
	s, ctx := newTestStore(t)
	source := uniqueSource("itest-rollup")
	base := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) // minute boundary

	// 120 one-second readings: value i at base+i seconds.
	var batch []Reading
	for i := 0; i < 120; i++ {
		batch = append(batch, Reading{Source: source, Metric: "HF", TS: base.Add(time.Duration(i) * time.Second), Value: float64(i)})
	}
	if _, err := s.InsertReadings(ctx, batch); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Materialize the continuous aggregate over this range (cannot run in a tx).
	if _, err := s.pool.Exec(ctx,
		"CALL refresh_continuous_aggregate('readings_1m', $1::timestamptz, $2::timestamptz)",
		base.Add(-time.Hour), base.Add(time.Hour)); err != nil {
		t.Fatalf("refresh cagg: %v", err)
	}

	// A 1-minute step routes to the rollup. Expect 2 buckets: avg(0..59)=29.5,
	// avg(60..119)=89.5 — proving the weighted re-bucketing is exact.
	buckets, err := s.QuerySeries(ctx, source, "HF", base, base.Add(2*time.Minute), "1m")
	if err != nil {
		t.Fatalf("rollup query: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d rollup buckets, want 2", len(buckets))
	}
	if buckets[0].Value != 29.5 || buckets[1].Value != 89.5 {
		t.Fatalf("rollup values = %v/%v, want 29.5/89.5", buckets[0].Value, buckets[1].Value)
	}
}

func TestIntegration_Rollup1sSeries(t *testing.T) {
	s, ctx := newTestStore(t)
	source := uniqueSource("itest-rollup1s")
	base := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	// 10 one-second readings: value i at base+i seconds.
	var batch []Reading
	for i := 0; i < 10; i++ {
		batch = append(batch, Reading{Source: source, Metric: "HF", TS: base.Add(time.Duration(i) * time.Second), Value: float64(i)})
	}
	if _, err := s.InsertReadings(ctx, batch); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.pool.Exec(ctx,
		"CALL refresh_continuous_aggregate('readings_1s', $1::timestamptz, $2::timestamptz)",
		base.Add(-time.Hour), base.Add(time.Hour)); err != nil {
		t.Fatalf("refresh 1s cagg: %v", err)
	}

	// A 5-second step (1s..59s) routes to readings_1s. Expect 2 buckets:
	// avg(0..4)=2, avg(5..9)=7 — exact weighted re-bucketing of 1s buckets.
	buckets, err := s.QuerySeries(ctx, source, "HF", base, base.Add(10*time.Second), "5s")
	if err != nil {
		t.Fatalf("1s rollup query: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2", len(buckets))
	}
	if buckets[0].Value != 2 || buckets[1].Value != 7 {
		t.Fatalf("1s rollup values = %v/%v, want 2/7", buckets[0].Value, buckets[1].Value)
	}
}

func TestIntegration_ConfigAndThresholds(t *testing.T) {
	s, ctx := newTestStore(t)

	key := uniqueSource("itest-cfg")
	if err := s.SetConfigValue(ctx, key, "7"); err != nil {
		t.Fatalf("set config: %v", err)
	}
	got, err := s.GetConfigValue(ctx, key)
	if err != nil || got != "7" {
		t.Fatalf("get config = %q,%v want 7", got, err)
	}
	// Upsert overwrites.
	if err := s.SetConfigValue(ctx, key, "9"); err != nil {
		t.Fatalf("update config: %v", err)
	}
	if got, _ := s.GetConfigValue(ctx, key); got != "9" {
		t.Fatalf("get config after update = %q, want 9", got)
	}

	metric := uniqueSource("ITESTMETRIC")
	max := 1.0e-7
	if err := s.UpsertThreshold(ctx, Threshold{Metric: metric, Max: &max, Enabled: true}); err != nil {
		t.Fatalf("upsert threshold: %v", err)
	}
	thresholds, err := s.GetThresholds(ctx)
	if err != nil {
		t.Fatalf("get thresholds: %v", err)
	}
	found := false
	for _, th := range thresholds {
		if th.Metric == metric {
			found = true
			if th.Max == nil || *th.Max != max || !th.Enabled {
				t.Fatalf("threshold = %+v, want max=%v enabled", th, max)
			}
		}
	}
	if !found {
		t.Fatalf("upserted threshold %q not returned", metric)
	}
}

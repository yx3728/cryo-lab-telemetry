package api

import (
	"math"
	"net/http/httptest"
	"testing"
	"time"
)

func TestValidateReading(t *testing.T) {
	ts := time.Date(2026, 6, 18, 22, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		reading ingestReading
		wantErr bool
	}{
		{"valid", ingestReading{Metric: "OC", TS: ts, Value: 2.3e-9}, false},
		{"valid with matching source", ingestReading{Source: "unisoku-stm", Metric: "OC", TS: ts, Value: 1}, false},
		{"missing metric", ingestReading{TS: ts, Value: 1}, true},
		{"source mismatch", ingestReading{Source: "evil", Metric: "OC", TS: ts, Value: 1}, true},
		{"zero ts", ingestReading{Metric: "OC", Value: 1}, true},
		{"NaN value", ingestReading{Metric: "OC", TS: ts, Value: math.NaN()}, true},
		{"Inf value", ingestReading{Metric: "OC", TS: ts, Value: math.Inf(1)}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validateReading(tc.reading, "unisoku-stm")
			if (got != "") != tc.wantErr {
				t.Fatalf("validateReading() = %q, wantErr=%v", got, tc.wantErr)
			}
		})
	}
}

func TestResolveStep(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name      string
		requested string
		span      time.Duration
		want      string
	}{
		{"explicit valid step honoured", "10s", time.Hour, "10s"},
		{"explicit minutes honoured", "2m", 48 * time.Hour, "2m"},
		{"invalid step falls back", "; DROP TABLE", time.Hour, "5s"},
		{"auto 1h span", "", time.Hour, "5s"},
		{"auto 3h span", "", 3 * time.Hour, "30s"},
		{"auto 24h span", "", 24 * time.Hour, "2m"},
		{"auto 7d span", "", 7 * 24 * time.Hour, "15m"},
		{"auto 30d span", "", 30 * 24 * time.Hour, "1h"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveStep(tc.requested, now.Add(-tc.span), now)
			if got != tc.want {
				t.Fatalf("resolveStep(%q, span=%s) = %q, want %q", tc.requested, tc.span, got, tc.want)
			}
		})
	}
}

func TestParseTimeRange(t *testing.T) {
	t.Run("defaults to last 3h", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/series?source=a&metric=b", nil)
		from, to, errMsg := parseTimeRange(r)
		if errMsg != "" {
			t.Fatalf("unexpected error: %s", errMsg)
		}
		span := to.Sub(from)
		if span < defaultRange-time.Minute || span > defaultRange+time.Minute {
			t.Fatalf("default span = %s, want ~%s", span, defaultRange)
		}
	})

	t.Run("rejects from after to", func(t *testing.T) {
		r := httptest.NewRequest("GET",
			"/api/series?from=2026-06-18T23:00:00Z&to=2026-06-18T22:00:00Z", nil)
		if _, _, errMsg := parseTimeRange(r); errMsg == "" {
			t.Fatal("expected error when from is after to")
		}
	})

	t.Run("rejects malformed time", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/series?from=not-a-time", nil)
		if _, _, errMsg := parseTimeRange(r); errMsg == "" {
			t.Fatal("expected error for malformed 'from'")
		}
	})
}

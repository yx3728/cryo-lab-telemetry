package alert

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/yx3728/lab-monitor/server/internal/store"
)

func f(v float64) *float64 { return &v }

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name        string
		threshold   store.Threshold
		value       float64
		wantCrossed bool
		wantKind    string
	}{
		{"above max", store.Threshold{Metric: "STM", Max: f(20), Enabled: true}, 25, true, "max"},
		{"at max (not over)", store.Threshold{Metric: "STM", Max: f(20), Enabled: true}, 20, false, ""},
		{"below min", store.Threshold{Metric: "X", Min: f(4), Enabled: true}, 3, true, "min"},
		{"within bounds", store.Threshold{Metric: "X", Min: f(4), Max: f(20), Enabled: true}, 10, false, ""},
		{"disabled is never crossed", store.Threshold{Metric: "STM", Max: f(20), Enabled: false}, 999, false, ""},
		{"no bounds set", store.Threshold{Metric: "X", Enabled: true}, 999, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cross, crossed := Evaluate(tc.threshold, tc.value)
			if crossed != tc.wantCrossed {
				t.Fatalf("crossed = %v, want %v", crossed, tc.wantCrossed)
			}
			if crossed && cross.Kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q", cross.Kind, tc.wantKind)
			}
		})
	}
}

// TestDebounce drives the debounce window with an injected clock. The Alerter's
// debounce state lives entirely in memory (no DB), so we can pass a nil store.
func TestDebounce(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := New(nil, 60*time.Second, log)

	current := time.Unix(1_000_000, 0)
	a.now = func() time.Time { return current }

	if a.debounced("STM") {
		t.Fatal("first cross should not be debounced")
	}
	// 30s later: still within the 60s window -> suppressed.
	current = current.Add(30 * time.Second)
	if !a.debounced("STM") {
		t.Fatal("second cross within window should be debounced")
	}
	// A different metric is independent.
	if a.debounced("OC") {
		t.Fatal("first cross for a different metric should not be debounced")
	}
	// 61s after the first fire: window elapsed -> re-armed.
	current = current.Add(31 * time.Second)
	if a.debounced("STM") {
		t.Fatal("cross after the debounce window should re-arm (not be debounced)")
	}
}

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

// TestEdgeTrigger verifies one event on the up-cross (alert), silence while it
// stays violating, and one event on the down-cross (recovered). The state
// decision is pure (no DB), so a nil store is fine.
func TestEdgeTrigger(t *testing.T) {
	a := New(nil, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	thr := store.Threshold{Metric: "OC", Max: f(1e-8), Enabled: true}

	step := func(value float64) string {
		cross, crossed := Evaluate(thr, value)
		event, _, _ := a.transition("OC", thr, cross, crossed)
		return event
	}

	if got := step(2e-9); got != "" {
		t.Fatalf("in-range reading should emit nothing, got %q", got)
	}
	if got := step(5e-8); got != "alert" {
		t.Fatalf("crossing above should emit 'alert', got %q", got)
	}
	if got := step(6e-8); got != "" {
		t.Fatalf("staying above should emit nothing (no repeat), got %q", got)
	}
	if got := step(3e-9); got != "recovered" {
		t.Fatalf("dropping back should emit 'recovered', got %q", got)
	}
	if got := step(2e-9); got != "" {
		t.Fatalf("staying in-range should emit nothing, got %q", got)
	}
	if got := step(9e-8); got != "alert" {
		t.Fatalf("re-crossing should emit 'alert' again, got %q", got)
	}
}

func TestNotifyCap(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := New(nil, 2, log) // cap 2 notifications/day
	current := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return current }

	if !a.allowNotify() || !a.allowNotify() {
		t.Fatal("first two notifications should be allowed")
	}
	if a.allowNotify() {
		t.Fatal("third notification should be capped")
	}
	// New UTC day resets the cap.
	current = current.Add(24 * time.Hour)
	if !a.allowNotify() {
		t.Fatal("cap should reset on a new day")
	}
}

func TestNotifyCapUnlimited(t *testing.T) {
	a := New(nil, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i := 0; i < 500; i++ {
		if !a.allowNotify() {
			t.Fatal("cap of 0 means unlimited")
		}
	}
}

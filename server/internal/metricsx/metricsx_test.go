package metricsx

import (
	"testing"
	"time"
)

func TestTrackerCounters(t *testing.T) {
	current := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return current })

	tr.RecordIngest("unisoku-stm", 7)
	tr.RecordIngest("unisoku-stm", 3)
	tr.RecordIngest("stm-fast", 5)

	current = current.Add(42 * time.Second)
	uptime, total, sources := tr.Snapshot()

	if total != 15 {
		t.Fatalf("total = %d, want 15", total)
	}
	if uptime != 42*time.Second {
		t.Fatalf("uptime = %s, want 42s", uptime)
	}
	if got := sources["unisoku-stm"].RowsSinceStart; got != 10 {
		t.Fatalf("unisoku-stm rows = %d, want 10", got)
	}
	if got := sources["stm-fast"].RowsSinceStart; got != 5 {
		t.Fatalf("stm-fast rows = %d, want 5", got)
	}
	if sources["unisoku-stm"].LastReceived == nil {
		t.Fatal("expected last_received to be set")
	}
}

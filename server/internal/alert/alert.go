// Package alert evaluates incoming readings against per-metric thresholds and
// dispatches debounced notifications (email and/or Slack). Threshold crosses are
// always recorded in alert_log, even when no notifier is configured, so the
// feature is observable without secrets.
package alert

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/yx3728/lab-monitor/server/internal/store"
)

// thresholdReloadInterval is how often the alerter refreshes its cached
// thresholds from the database, so admin edits take effect without a per-ingest
// query on the hot path. PUT /api/config also calls Reload for immediacy.
const thresholdReloadInterval = 15 * time.Second

// Notifier delivers an alert message. Implementations (email, Slack) are
// optional; a nil/absent notifier is simply skipped.
type Notifier interface {
	Send(ctx context.Context, subject, body string) error
}

// Alerter holds cached thresholds, debounce state, and the configured notifiers.
type Alerter struct {
	store     *store.Store
	debounce  time.Duration
	notifiers []Notifier
	log       *slog.Logger
	now       func() time.Time

	mu         sync.RWMutex
	thresholds map[string]store.Threshold
	lastFired  map[string]time.Time
}

// New constructs an Alerter. notifiers may be empty (log-only mode).
func New(s *store.Store, debounce time.Duration, log *slog.Logger, notifiers ...Notifier) *Alerter {
	return &Alerter{
		store:      s,
		debounce:   debounce,
		notifiers:  notifiers,
		log:        log,
		now:        time.Now,
		thresholds: make(map[string]store.Threshold),
		lastFired:  make(map[string]time.Time),
	}
}

// Start loads thresholds once and then refreshes them periodically until ctx is
// cancelled. Call it in a goroutine from main.
func (a *Alerter) Start(ctx context.Context) {
	a.Reload(ctx)
	ticker := time.NewTicker(thresholdReloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.Reload(ctx)
		}
	}
}

// Reload refreshes the cached threshold map from the database.
func (a *Alerter) Reload(ctx context.Context) {
	ts, err := a.store.GetThresholds(ctx)
	if err != nil {
		a.log.Error("alert: reload thresholds failed", "err", err)
		return
	}
	m := make(map[string]store.Threshold, len(ts))
	for _, t := range ts {
		m[t.Metric] = t
	}
	a.mu.Lock()
	a.thresholds = m
	a.mu.Unlock()
}

// Cross describes a single threshold violation.
type Cross struct {
	Kind           string  // "min" or "max"
	ThresholdValue float64 // the bound that was crossed
}

// Evaluate is the pure decision: does value violate the threshold? It is
// exported so the alerting rule can be unit-tested without any I/O.
func Evaluate(t store.Threshold, value float64) (Cross, bool) {
	if !t.Enabled {
		return Cross{}, false
	}
	if t.Max != nil && value > *t.Max {
		return Cross{Kind: "max", ThresholdValue: *t.Max}, true
	}
	if t.Min != nil && value < *t.Min {
		return Cross{Kind: "min", ThresholdValue: *t.Min}, true
	}
	return Cross{}, false
}

// Check evaluates a batch of readings, logging and (debounced) dispatching any
// threshold crosses. It never blocks the caller on network I/O — notifications
// are sent from a background goroutine.
func (a *Alerter) Check(ctx context.Context, readings []store.Reading) {
	for _, r := range readings {
		a.mu.RLock()
		t, ok := a.thresholds[r.Metric]
		a.mu.RUnlock()
		if !ok {
			continue
		}
		cross, crossed := Evaluate(t, r.Value)
		if !crossed {
			continue
		}
		if a.debounced(r.Metric) {
			continue
		}
		a.fire(ctx, r, cross)
	}
}

// debounced reports whether this metric alerted within the debounce window, and
// records "now" as the last-fired time if it did not.
func (a *Alerter) debounced(metric string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if last, ok := a.lastFired[metric]; ok && now.Sub(last) < a.debounce {
		return true
	}
	a.lastFired[metric] = now
	return false
}

func (a *Alerter) fire(ctx context.Context, r store.Reading, cross Cross) {
	notified := len(a.notifiers) > 0
	if err := a.store.InsertAlertLog(ctx, store.AlertEvent{
		Source:         r.Source,
		Metric:         r.Metric,
		Value:          r.Value,
		Kind:           cross.Kind,
		ThresholdValue: cross.ThresholdValue,
		Notified:       notified,
	}); err != nil {
		a.log.Error("alert: write alert_log failed", "err", err)
	}

	subject := fmt.Sprintf("[lab-monitor] %s %s threshold crossed", r.Metric, cross.Kind)
	body := fmt.Sprintf(
		"Source %s metric %s read %g, crossing the %s threshold of %g at %s.",
		r.Source, r.Metric, r.Value, cross.Kind, cross.ThresholdValue, r.TS.Format(time.RFC3339))
	a.log.Warn("alert fired", "metric", r.Metric, "kind", cross.Kind,
		"value", r.Value, "threshold", cross.ThresholdValue)

	if !notified {
		return
	}
	// Dispatch off the ingest path so SMTP/Slack latency never slows ingest.
	for _, n := range a.notifiers {
		n := n
		go func() {
			sendCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := n.Send(sendCtx, subject, body); err != nil {
				a.log.Error("alert: notifier send failed", "err", err)
			}
		}()
	}
}

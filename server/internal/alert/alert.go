// Package alert evaluates incoming readings against per-metric thresholds and
// dispatches EDGE-TRIGGERED notifications: one email when a metric crosses out of
// its safe range (alert), and one when it returns (recovered) — not one per
// reading. Every transition is recorded in alert_log, even with no notifier
// configured, so the feature is observable without secrets.
package alert

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/yx3728/lab-monitor/server/internal/store"
)

// reloadInterval is how often the alerter refreshes its cached thresholds and the
// admin-set daily email cap from the database. PUT /api/config also calls Reload
// for immediacy.
const reloadInterval = 15 * time.Second

// maxEmailsConfigKey is the config row the admin panel edits for the daily cap.
const maxEmailsConfigKey = "alert_max_emails_per_day"

// Notifier delivers an alert message. Implementations (email, Slack) are
// optional; a nil/absent notifier is simply skipped.
type Notifier interface {
	Send(ctx context.Context, subject, body string) error
}

// Alerter holds cached thresholds, per-metric alarm state, and the notifiers.
type Alerter struct {
	store     *store.Store
	notifiers []Notifier
	log       *slog.Logger
	now       func() time.Time

	mu         sync.RWMutex
	thresholds map[string]store.Threshold
	maxPerDay  int               // dispatch cap per UTC day (0 = unlimited); refreshed from config
	state      map[string]string // metric -> "" (in range) | "max" | "min" (currently violating)
	notifyDay  string            // UTC date of the current notification count
	notifyN    int               // notifications dispatched today
}

// New constructs an Alerter. notifiers may be empty (log-only mode).
// defaultMaxPerDay is the fallback daily email cap used until/if the admin sets
// one in the dashboard (persisted in config); 0 = unlimited.
func New(s *store.Store, defaultMaxPerDay int, log *slog.Logger, notifiers ...Notifier) *Alerter {
	return &Alerter{
		store:      s,
		notifiers:  notifiers,
		log:        log,
		now:        time.Now,
		thresholds: make(map[string]store.Threshold),
		maxPerDay:  defaultMaxPerDay,
		state:      make(map[string]string),
	}
}

// Start loads config once and then refreshes it periodically until ctx is
// cancelled. Call it in a goroutine from main.
func (a *Alerter) Start(ctx context.Context) {
	a.Reload(ctx)
	ticker := time.NewTicker(reloadInterval)
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

// Reload refreshes the cached thresholds and the daily email cap from the DB.
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
	if v, err := a.store.GetConfigValue(ctx, maxEmailsConfigKey); err == nil {
		if n, perr := strconv.Atoi(v); perr == nil && n >= 0 {
			a.maxPerDay = n
		}
	}
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

// Check evaluates a batch of readings and emits a notification only on a state
// transition (in-range ⇄ violating). It never blocks the caller on network I/O.
func (a *Alerter) Check(ctx context.Context, readings []store.Reading) {
	for _, r := range readings {
		a.mu.RLock()
		t, ok := a.thresholds[r.Metric]
		a.mu.RUnlock()
		if !ok {
			continue
		}
		cross, crossed := Evaluate(t, r.Value)
		a.handleTransition(ctx, r, t, cross, crossed)
	}
}

// handleTransition emits a notification only when the reading changes the
// metric's alarm state. The state decision (under the lock) is separated from the
// slow fire so the edge logic is unit-testable without a DB.
func (a *Alerter) handleTransition(ctx context.Context, r store.Reading, t store.Threshold, cross Cross, crossed bool) {
	event, kind, thr := a.transition(r.Metric, t, cross, crossed)
	if event != "" {
		a.fire(ctx, r, kind, thr, event == "recovered")
	}
}

// transition updates the metric's alarm state and returns the event to emit:
// "alert" on entering an alarm (in-range → violating, or flipped min↔max),
// "recovered" on returning to the safe range, or "" while the state is unchanged.
func (a *Alerter) transition(metric string, t store.Threshold, cross Cross, crossed bool) (event, kind string, thr float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	prev := a.state[metric]
	switch {
	case crossed && prev != cross.Kind:
		a.state[metric] = cross.Kind
		return "alert", cross.Kind, cross.ThresholdValue
	case !crossed && prev != "":
		a.state[metric] = ""
		return "recovered", prev, boundValue(t, prev)
	}
	return "", "", 0
}

func boundValue(t store.Threshold, kind string) float64 {
	if kind == "max" && t.Max != nil {
		return *t.Max
	}
	if kind == "min" && t.Min != nil {
		return *t.Min
	}
	return 0
}

// allowNotify enforces the per-UTC-day notification cap. It returns true (and
// counts the dispatch) when under the cap, false when the cap is reached. A cap
// of 0 means unlimited. The counter resets at UTC midnight.
func (a *Alerter) allowNotify() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.maxPerDay <= 0 {
		return true
	}
	day := a.now().UTC().Format("2006-01-02")
	if day != a.notifyDay {
		a.notifyDay = day
		a.notifyN = 0
	}
	if a.notifyN >= a.maxPerDay {
		return false
	}
	a.notifyN++
	return true
}

// fire records the transition in alert_log and, if a notifier is configured and
// the daily cap allows, dispatches one email. `recovery` selects the wording and
// the alert_log kind ("recovered" vs the bound).
func (a *Alerter) fire(ctx context.Context, r store.Reading, kind string, thresholdValue float64, recovery bool) {
	notified := len(a.notifiers) > 0 && a.allowNotify()

	logKind := kind
	if recovery {
		logKind = "recovered"
	}
	if err := a.store.InsertAlertLog(ctx, store.AlertEvent{
		Source:         r.Source,
		Metric:         r.Metric,
		Value:          r.Value,
		Kind:           logKind,
		ThresholdValue: thresholdValue,
		Notified:       notified,
	}); err != nil {
		a.log.Error("alert: write alert_log failed", "err", err)
	}

	var subject, body string
	if recovery {
		subject = fmt.Sprintf("[lab-monitor] %s back to normal", r.Metric)
		body = fmt.Sprintf("%s on %s read %g — back within the %s threshold of %g at %s.",
			r.Metric, r.Source, r.Value, kind, thresholdValue, r.TS.Format(time.RFC3339))
	} else {
		subject = fmt.Sprintf("[lab-monitor] %s %s threshold crossed", r.Metric, kind)
		body = fmt.Sprintf("%s on %s read %g — crossing the %s threshold of %g at %s.",
			r.Metric, r.Source, r.Value, kind, thresholdValue, r.TS.Format(time.RFC3339))
	}
	a.log.Warn("alert transition", "metric", r.Metric, "event", logKind,
		"value", r.Value, "threshold", thresholdValue, "notified", notified)

	if !notified {
		if len(a.notifiers) > 0 {
			a.log.Warn("alert email suppressed: daily cap reached",
				"metric", r.Metric, "cap_per_day", a.maxPerDay)
		}
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

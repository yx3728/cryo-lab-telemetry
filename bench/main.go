// Command bench is the load + reliability harness for the lab-monitor system.
//
// It is meant to run from a workstation against the DEPLOYED box (real network,
// real 2 GB t4g.small) so the numbers are honest — never localhost.
//
// Subcommands:
//
//	ingest    — one logical producer at a target readings/sec, configurable batch
//	            size; reports p50/p99 latency, errors, achieved throughput, and how
//	            many rows actually landed in the DB (via /metrics).
//	producers — N concurrent producers, each with its own source token (from a
//	            tokens JSON file), each at a per-producer rate; aggregate metrics.
//	read      — M concurrent clients against /api/series with random ranges; p99.
//
// Load model (all subcommands): an open-loop scheduler assigns each request an
// intended send time (start + i/rate). Workers pull the next index, sleep until
// its scheduled time, then send. Latency is measured from the INTENDED time, so
// when the system can't keep up the growing queueing delay shows up in the tail
// (Gil Tene's coordinated-omission correction) rather than being hidden.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: bench <ingest|producers|read> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "ingest":
		cmdIngest(os.Args[2:])
	case "producers":
		cmdProducers(os.Args[2:])
	case "read":
		cmdRead(os.Args[2:])
	default:
		fmt.Printf("unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}

// --- shared HTTP + timing helpers -------------------------------------------

func httpClient() *http.Client {
	t := &http.Transport{
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 512,
		MaxConnsPerHost:     0, // unlimited
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true, // measure server compute, not client gunzip
		// Force HTTP/1.1 (a dedicated connection per in-flight request) instead of
		// HTTP/2 multiplexing everything over one socket — this models many
		// independent producers/viewers more faithfully and avoids h2 flow-control
		// stalls that otherwise hang large concurrent GETs against Caddy.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	return &http.Client{Transport: t, Timeout: 30 * time.Second}
}

// latencyStats collects durations and computes percentiles.
type latencyStats struct {
	mu      sync.Mutex
	samples []time.Duration
	ok      int64
	errs    int64
}

func (l *latencyStats) add(d time.Duration, err error) {
	l.mu.Lock()
	l.samples = append(l.samples, d)
	l.mu.Unlock()
	if err != nil {
		atomic.AddInt64(&l.errs, 1)
	} else {
		atomic.AddInt64(&l.ok, 1)
	}
}

func (l *latencyStats) pct(p float64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.samples) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), l.samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(math.Ceil(p/100*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

// runLoad drives `total` requests at `rate` req/s using `concurrency` workers,
// calling send(i) for request i. Returns wall-clock duration and the stats.
func runLoad(total int, rate float64, concurrency int, send func(i int) (time.Duration, error)) (time.Duration, *latencyStats) {
	stats := &latencyStats{}
	var next int64
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(atomic.AddInt64(&next, 1)) - 1
				if i >= total {
					return
				}
				scheduled := start.Add(time.Duration(float64(i) / rate * float64(time.Second)))
				if d := time.Until(scheduled); d > 0 {
					time.Sleep(d)
				}
				_, err := send(i)
				// Latency from the INTENDED time corrects for coordinated omission.
				stats.add(time.Since(scheduled), err)
			}
		}()
	}
	wg.Wait()
	return time.Since(start), stats
}

// reading is one ingest reading.
type reading struct {
	Source string  `json:"source"`
	Metric string  `json:"metric"`
	TS     string  `json:"ts"`
	Value  float64 `json:"value"`
}

// global monotonic sequence so every generated reading has a unique timestamp
// within a run (so idempotent dedup never silently eats distinct readings).
var seq int64

func makeBatch(client *http.Client, base, source, token, metric string, n int) func(i int) (time.Duration, error) {
	url := base + "/ingest"
	// Base each process's timestamps at "now" so distinct bench runs occupy
	// distinct timestamp ranges; combined with the global seq this makes every
	// reading's (source, metric, ts) unique, so idempotent dedup never silently
	// drops a benchmark's data and db_rows_delta is a true "landed" count.
	epoch := time.Now()
	return func(_ int) (time.Duration, error) {
		batch := make([]reading, n)
		for k := 0; k < n; k++ {
			s := atomic.AddInt64(&seq, 1)
			batch[k] = reading{
				Source: source, Metric: metric,
				TS:    epoch.Add(time.Duration(s) * time.Microsecond).Format(time.RFC3339Nano),
				Value: float64(s % 1000),
			}
		}
		body, _ := json.Marshal(batch)
		t0 := time.Now()
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Api-Key", token)
		resp, err := client.Do(req)
		if err != nil {
			return time.Since(t0), err
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return time.Since(t0), fmt.Errorf("status %d", resp.StatusCode)
		}
		return time.Since(t0), nil
	}
}

// dbTotalRows scrapes /metrics for total_rows_in_db (to confirm landed rows).
func dbTotalRows(client *http.Client, base string) int64 {
	resp, err := client.Get(base + "/metrics")
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	var m struct {
		Total int64 `json:"total_rows_in_db"`
	}
	if json.NewDecoder(resp.Body).Decode(&m) != nil {
		return -1
	}
	return m.Total
}

func tokensFromFile(path string) []struct {
	Source string `json:"source"`
	Token  string `json:"token"`
} {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("read tokens file: %v\n", err)
		os.Exit(1)
	}
	var out []struct {
		Source string `json:"source"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		fmt.Printf("parse tokens file: %v\n", err)
		os.Exit(1)
	}
	return out
}

func printResult(label string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Printf("\n=== %s ===\n%s\n", label, string(b))
}

var _ = context.Background // reserved for future cancellation

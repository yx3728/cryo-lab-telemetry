package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// --- ingest: single producer ------------------------------------------------

func cmdIngest(args []string) {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	base := fs.String("base", "https://3.220.132.187.sslip.io", "deployed base URL")
	source := fs.String("source", "bench", "source name (must match the token)")
	token := fs.String("token", "", "ingest API token for the source")
	metric := fs.String("metric", "bench", "metric name")
	rate := fs.Float64("rate", 100, "target readings per second")
	batch := fs.Int("batch", 1, "readings per request (1 = unbatched)")
	duration := fs.Float64("duration", 20, "seconds")
	concurrency := fs.Int("concurrency", 64, "max in-flight requests")
	fs.String("csv", "", "append result JSON to this file")
	fs.Parse(args)

	client := httpClient()
	reqRate := *rate / float64(*batch)
	totalReq := int(reqRate * *duration)
	if totalReq < 1 {
		totalReq = 1
	}

	before := dbTotalRows(client, *base)
	wall, stats := runLoad(totalReq, reqRate, *concurrency,
		makeBatch(client, *base, *source, *token, *metric, *batch))
	time.Sleep(1 * time.Second) // let async work settle before reading the count
	after := dbTotalRows(client, *base)

	sentReadings := int64(totalReq) * int64(*batch)
	res := map[string]any{
		"mode":                    "ingest",
		"target_readings_per_s":   *rate,
		"batch":                   *batch,
		"requests_sent":           totalReq,
		"readings_sent":           sentReadings,
		"ok":                      stats.ok,
		"errors":                  stats.errs,
		"error_rate":              ratio(stats.errs, int64(totalReq)),
		"wall_s":                  round(wall.Seconds(), 2),
		"achieved_req_per_s":      round(float64(totalReq)/wall.Seconds(), 1),
		"achieved_readings_per_s": round(float64(sentReadings)/wall.Seconds(), 1),
		"p50_ms":                  round(ms(stats.pct(50)), 1),
		"p99_ms":                  round(ms(stats.pct(99)), 1),
		"max_ms":                  round(ms(stats.pct(100)), 1),
		"db_rows_delta":           after - before,
	}
	printResult("ingest", res)
	maybeCSV(fs, res)
}

// --- producers: N concurrent producers --------------------------------------

func cmdProducers(args []string) {
	fs := flag.NewFlagSet("producers", flag.ExitOnError)
	base := fs.String("base", "https://3.220.132.187.sslip.io", "deployed base URL")
	tokensFile := fs.String("tokens", "tokens.local.json", "JSON [{source,token}] file")
	n := fs.Int("n", 5, "number of concurrent producers")
	rate := fs.Float64("rate", 50, "readings per second PER producer")
	batch := fs.Int("batch", 10, "readings per request")
	duration := fs.Float64("duration", 20, "seconds")
	concurrencyPer := fs.Int("concurrency-per", 8, "in-flight requests per producer")
	fs.String("csv", "", "append result JSON to this file")
	fs.Parse(args)

	client := httpClient()
	toks := tokensFromFile(*tokensFile)
	if *n > len(toks) {
		fmt.Printf("requested %d producers but only %d tokens available\n", *n, len(toks))
		os.Exit(1)
	}

	reqRate := *rate / float64(*batch)
	totalReqPer := int(reqRate * *duration)
	if totalReqPer < 1 {
		totalReqPer = 1
	}

	before := dbTotalRows(client, *base)
	merged := &latencyStats{}
	var wg sync.WaitGroup
	start := time.Now()
	for p := 0; p < *n; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			_, st := runLoad(totalReqPer, reqRate, *concurrencyPer,
				makeBatch(client, *base, toks[p].Source, toks[p].Token, "bench", *batch))
			merged.mu.Lock()
			merged.samples = append(merged.samples, st.samples...)
			merged.mu.Unlock()
			merged.ok += st.ok
			merged.errs += st.errs
		}(p)
	}
	wg.Wait()
	wall := time.Since(start)
	time.Sleep(1 * time.Second)
	after := dbTotalRows(client, *base)

	sentReadings := int64(*n) * int64(totalReqPer) * int64(*batch)
	res := map[string]any{
		"mode":                     "producers",
		"producers":                *n,
		"rate_per_producer":        *rate,
		"batch":                    *batch,
		"readings_sent":            sentReadings,
		"ok":                       merged.ok,
		"errors":                   merged.errs,
		"error_rate":               ratio(merged.errs, merged.ok+merged.errs),
		"wall_s":                   round(wall.Seconds(), 2),
		"aggregate_readings_per_s": round(float64(sentReadings)/wall.Seconds(), 1),
		"p50_ms":                   round(ms(merged.pct(50)), 1),
		"p99_ms":                   round(ms(merged.pct(99)), 1),
		"max_ms":                   round(ms(merged.pct(100)), 1),
		"db_rows_delta":            after - before,
	}
	printResult("producers", res)
	maybeCSV(fs, res)
}

// --- read: M concurrent viewers ---------------------------------------------

func cmdRead(args []string) {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	base := fs.String("base", "https://3.220.132.187.sslip.io", "deployed base URL")
	clients := fs.Int("clients", 50, "concurrent read clients")
	duration := fs.Float64("duration", 20, "seconds")
	srcFlag := fs.String("source", "", "source to read (default: auto-discover)")
	metricFlag := fs.String("metric", "", "metric to read (default: auto-discover)")
	fs.String("csv", "", "append result JSON to this file")
	fs.Parse(args)

	client := httpClient()
	source, metrics := *srcFlag, []string{*metricFlag}
	if *srcFlag == "" || *metricFlag == "" {
		source, metrics = discoverChannels(client, *base)
	}
	if source == "" || len(metrics) == 0 {
		fmt.Println("no channels found to read")
		os.Exit(1)
	}
	ranges := []time.Duration{time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 40 * 24 * time.Hour}

	stats := &latencyStats{}
	start := time.Now()
	deadline := start.Add(time.Duration(*duration * float64(time.Second)))
	var wg sync.WaitGroup
	var reqs int64
	var mu sync.Mutex
	for c := 0; c < *clients; c++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(seed) + 1))
			for time.Now().Before(deadline) {
				metric := metrics[rng.Intn(len(metrics))]
				span := ranges[rng.Intn(len(ranges))]
				to := time.Now()
				from := to.Add(-span)
				q := url.Values{}
				q.Set("source", source)
				q.Set("metric", metric)
				q.Set("from", from.Format(time.RFC3339))
				q.Set("to", to.Format(time.RFC3339))
				u := *base + "/api/series?" + q.Encode()
				t0 := time.Now()
				resp, err := client.Get(u)
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode >= 300 {
						err = fmt.Errorf("status %d", resp.StatusCode)
					}
				}
				stats.add(time.Since(t0), err)
				mu.Lock()
				reqs++
				mu.Unlock()
			}
		}(c)
	}
	wg.Wait()
	wall := time.Since(start)

	res := map[string]any{
		"mode":               "read",
		"clients":            *clients,
		"requests":           reqs,
		"ok":                 stats.ok,
		"errors":             stats.errs,
		"error_rate":         ratio(stats.errs, reqs),
		"achieved_req_per_s": round(float64(reqs)/wall.Seconds(), 1),
		"p50_ms":             round(ms(stats.pct(50)), 1),
		"p99_ms":             round(ms(stats.pct(99)), 1),
		"max_ms":             round(ms(stats.pct(100)), 1),
	}
	printResult("read", res)
	maybeCSV(fs, res)
}

func discoverChannels(client *http.Client, base string) (string, []string) {
	resp, err := client.Get(base + "/api/channels")
	if err != nil {
		return "", nil
	}
	defer resp.Body.Close()
	var data struct {
		Channels []struct {
			Source string `json:"source"`
			Metric string `json:"metric"`
		} `json:"channels"`
	}
	if json.NewDecoder(resp.Body).Decode(&data) != nil || len(data.Channels) == 0 {
		return "", nil
	}
	source := data.Channels[0].Source
	var metrics []string
	for _, c := range data.Channels {
		if c.Source == source {
			metrics = append(metrics, c.Metric)
		}
	}
	return source, metrics
}

// --- small helpers ----------------------------------------------------------

func ratio(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return round(float64(a)/float64(b), 4)
}

func round(f float64, places int) float64 {
	p := 1.0
	for i := 0; i < places; i++ {
		p *= 10
	}
	return float64(int64(f*p+0.5)) / p
}

// maybeCSV appends a one-line JSON record to the file named by a -csv flag, if set.
func maybeCSV(fs *flag.FlagSet, res map[string]any) {
	f := fs.Lookup("csv")
	if f == nil || f.Value.String() == "" {
		return
	}
	line, _ := json.Marshal(res)
	fh, err := os.OpenFile(f.Value.String(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	fmt.Fprintln(fh, string(line))
}

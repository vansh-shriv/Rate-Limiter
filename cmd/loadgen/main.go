// Command loadgen is an open-loop HTTP load generator for the rate limiter.
//
// Open loop: requests are *scheduled* at a fixed rate regardless of how fast the server answers, and each
// latency is measured from the request's INTENDED send time. If the server (or this tool) falls behind,
// the queueing delay shows up in the percentiles instead of being silently omitted ("coordinated omission",
// which flatters closed-loop tools and makes p99 claims meaningless).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type sample struct {
	intended time.Duration // offset from start
	latency  time.Duration
	status   int // 0 = transport error
}

type Report struct {
	Target        string  `json:"target"`
	TargetRPS     int     `json:"target_rps"`
	DurationSec   float64 `json:"duration_sec"`
	WarmupSec     float64 `json:"warmup_sec"`
	Workers       int     `json:"workers"`
	Keys          int     `json:"keys"`
	Algorithm     string  `json:"algorithm,omitempty"`
	AchievedRPS   float64 `json:"achieved_rps"`
	Requests      int     `json:"requests"`
	Allowed200    int     `json:"status_200"`
	Denied429     int     `json:"status_429"`
	OtherStatus   int     `json:"status_other"`
	TransportErrs int     `json:"transport_errors"`
	Dropped       int64   `json:"dropped_overload"`
	MeanMs        float64 `json:"mean_ms"`
	P50Ms         float64 `json:"p50_ms"`
	P90Ms         float64 `json:"p90_ms"`
	P99Ms         float64 `json:"p99_ms"`
	P999Ms        float64 `json:"p99_9_ms"`
	MaxMs         float64 `json:"max_ms"`
	GoOS          string  `json:"goos"`
	NumCPU        int     `json:"num_cpu"`
	SLOP99Ms      float64 `json:"slo_p99_ms,omitempty"`
	SLOMet        *bool   `json:"slo_met,omitempty"`
}

func main() {
	var (
		url       = flag.String("url", "http://localhost:8080/v1/check", "decision endpoint")
		rps       = flag.Int("rps", 20000, "target requests per second (open loop)")
		duration  = flag.Duration("duration", 30*time.Second, "measured duration")
		warmup    = flag.Duration("warmup", 5*time.Second, "warmup excluded from results")
		workers   = flag.Int("workers", 256, "max in-flight requests / connections")
		keys      = flag.Int("keys", 10000, "distinct limiter keys to spread load over")
		zipf      = flag.Bool("zipf", false, "skewed (hot key) key distribution instead of uniform")
		apiKey    = flag.String("api-key", os.Getenv("LOADGEN_API_KEY"), "tenant API key (or env LOADGEN_API_KEY)")
		admin     = flag.String("admin-url", "", "if set with -admin-token, bootstrap a tenant+key first (e.g. http://localhost:8080)")
		adminTok  = flag.String("admin-token", os.Getenv("LOADGEN_ADMIN_TOKEN"), "admin bearer token for -admin-url")
		algo      = flag.String("algo", "token_bucket", "algorithm for the bootstrapped tenant")
		slo       = flag.Duration("slo-p99", 0, "if >0, exit 1 when measured p99 exceeds this")
		jsonOut   = flag.String("json", "", "write the report as JSON to this file")
		reqTO     = flag.Duration("timeout", 2*time.Second, "per-request timeout")
		queueSize = flag.Int("queue", 200000, "pending-request queue; overflow is counted as dropped")
	)
	flag.Parse()

	if *admin != "" {
		k, err := bootstrap(*admin, *adminTok, *algo)
		if err != nil {
			fatal("bootstrap: %v", err)
		}
		*apiKey = k
	}
	if *apiKey == "" {
		fatal("need -api-key (or -admin-url/-admin-token to create one)")
	}

	bodies := make([][]byte, *keys)
	for i := range bodies {
		bodies[i] = []byte(fmt.Sprintf(`{"key":"u%d"}`, i))
	}
	client := &http.Client{
		Timeout: *reqTO,
		Transport: &http.Transport{
			MaxIdleConns: *workers, MaxIdleConnsPerHost: *workers, MaxConnsPerHost: *workers,
			DisableCompression: true, IdleConnTimeout: 90 * time.Second,
		},
	}

	total := *warmup + *duration
	jobs := make(chan time.Duration, *queueSize)
	results := make([][]sample, *workers)
	var dropped atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()

	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(w), 42))
			var z *rand.Zipf
			if *zipf {
				z = rand.NewZipf(rng, 1.2, 1, uint64(*keys-1))
			}
			local := make([]sample, 0, (*rps)*int(total/time.Second)/(*workers)+1024)
			for intended := range jobs {
				idx := 0
				if z != nil {
					idx = int(z.Uint64())
				} else {
					idx = rng.IntN(*keys)
				}
				status := do(client, *url, *apiKey, bodies[idx])
				local = append(local, sample{intended, time.Since(start) - intended, status})
			}
			results[w] = local
		}(w)
	}

	// Pacer: release every request whose intended time has arrived. Sleeping coarsely is fine because
	// each job carries its intended time, so any pacing jitter is charged to latency, not hidden.
	interval := time.Second / time.Duration(*rps)
	next := time.Duration(0)
	for {
		now := time.Since(start)
		if now >= total {
			break
		}
		for next <= now {
			select {
			case jobs <- next:
			default:
				dropped.Add(1)
			}
			next += interval
		}
		time.Sleep(200 * time.Microsecond)
	}
	close(jobs)
	wg.Wait()

	rep := summarize(results, *warmup, *duration)
	rep.Target, rep.TargetRPS, rep.Workers, rep.Keys, rep.Dropped = *url, *rps, *workers, *keys, dropped.Load()
	rep.WarmupSec, rep.DurationSec = warmup.Seconds(), duration.Seconds()
	if *admin != "" {
		rep.Algorithm = *algo
	}
	if *slo > 0 {
		met := rep.P99Ms <= float64(*slo)/float64(time.Millisecond) && rep.TransportErrs == 0 && rep.Dropped == 0
		rep.SLOP99Ms, rep.SLOMet = float64(*slo)/float64(time.Millisecond), &met
	}
	print(rep)
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(*jsonOut, b, 0o644); err != nil {
			fatal("write json: %v", err)
		}
	}
	if rep.SLOMet != nil && !*rep.SLOMet {
		os.Exit(1)
	}
}

func do(c *http.Client, url, key string, body []byte) int {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("X-API-Key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// summarize keeps only requests whose INTENDED time falls inside the measured window.
func summarize(results [][]sample, warmup, duration time.Duration) Report {
	var lat []time.Duration
	var r Report
	for _, rs := range results {
		for _, s := range rs {
			if s.intended < warmup || s.intended >= warmup+duration {
				continue
			}
			r.Requests++
			switch {
			case s.status == 0:
				r.TransportErrs++
				continue // transport failures are reported, not mixed into latency
			case s.status == 200:
				r.Allowed200++
			case s.status == 429:
				r.Denied429++
			default:
				r.OtherStatus++
			}
			lat = append(lat, s.latency)
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	if n := len(lat); n > 0 {
		var sum time.Duration
		for _, d := range lat {
			sum += d
		}
		r.MeanMs = ms(sum / time.Duration(n))
		r.P50Ms, r.P90Ms = ms(percentile(lat, .50)), ms(percentile(lat, .90))
		r.P99Ms, r.P999Ms, r.MaxMs = ms(percentile(lat, .99)), ms(percentile(lat, .999)), ms(lat[n-1])
	}
	r.AchievedRPS = math.Round(float64(len(lat)) / duration.Seconds())
	r.GoOS, r.NumCPU = runtime.GOOS, runtime.NumCPU()
	return r
}

// percentile uses the nearest-rank method on a sorted slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[min(max(rank, 0), len(sorted)-1)]
}

func print(r Report) {
	fmt.Printf("target %d rps  achieved %.0f rps  (%d requests in %.0fs window, %d workers, %d keys)\n",
		r.TargetRPS, r.AchievedRPS, r.Requests, r.DurationSec, r.Workers, r.Keys)
	fmt.Printf("status: 200=%d 429=%d other=%d transport_errors=%d dropped_overload=%d\n",
		r.Allowed200, r.Denied429, r.OtherStatus, r.TransportErrs, r.Dropped)
	fmt.Printf("latency ms (from intended send time): mean=%.2f p50=%.2f p90=%.2f p99=%.2f p99.9=%.2f max=%.2f\n",
		r.MeanMs, r.P50Ms, r.P90Ms, r.P99Ms, r.P999Ms, r.MaxMs)
	if r.SLOMet != nil {
		verdict := "PASS"
		if !*r.SLOMet {
			verdict = "FAIL"
		}
		fmt.Printf("SLO p99 <= %.1f ms with 0 errors/drops: %s\n", r.SLOP99Ms, verdict)
	}
}

// bootstrap creates a high-limit tenant and API key through the admin API.
func bootstrap(adminURL, token, algo string) (string, error) {
	tenantID := "loadtest-" + strings.ReplaceAll(algo, "_", "-")
	limit := 1_000_000_000
	if algo == "sliding_window" {
		limit = 100_000 // sliding window memory is O(limit); the cap is 100k
	}
	rule := fmt.Sprintf(`{"name":"loadtest","rule":{"algorithm":%q,"limit":%d,"window":"1s"}}`, algo, limit)
	if err := adminCall("PUT", adminURL+"/admin/v1/tenants/"+tenantID, token, rule, nil); err != nil {
		return "", err
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := adminCall("POST", adminURL+"/admin/v1/tenants/"+tenantID+"/keys", token, "", &out); err != nil {
		return "", err
	}
	return out.Key, nil
}

func adminCall(method, url, token, body string, out any) error {
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, url, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "loadgen: "+f+"\n", a...)
	os.Exit(2)
}

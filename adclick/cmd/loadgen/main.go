// Command loadgen sends synthetic clicks to the API and reports latency.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

func main() {
	url := flag.String("url", "http://localhost:8080/v1/clicks", "click endpoint")
	rps := flag.Int("rps", 1000, "target requests per second")
	dur := flag.Duration("duration", 30*time.Second, "test length")
	workers := flag.Int("workers", 64, "concurrent senders")
	dup := flag.Float64("dup-rate", 0.02, "fraction of requests re-sent with the same event_id")
	flag.Parse()

	ads := []string{"ad-1001", "ad-1002", "ad-2001", "ad-2002", "ad-3001", "ad-3002", "ad-4001", "ad-4002"}
	// Skewed: ad-1001 is "hot" (this is what a Super Bowl ad looks like).
	zipf := rand.NewZipf(rand.New(rand.NewPCG(1, 2)), 1.3, 1, uint64(len(ads)-1))
	var zmu sync.Mutex
	pick := func() string { zmu.Lock(); defer zmu.Unlock(); return ads[zipf.Uint64()] }

	countries := []string{"US", "US", "US", "GB", "DE", "IN", "BR", "JP", "FR", "NZ"}
	devices := []string{"mobile", "mobile", "desktop", "tablet"}
	browsers := []string{"chrome", "safari", "firefox", "edge"}
	sources := []string{"web", "app", "email", "social"}

	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: *workers}}
	var ok, bad, fail, sent, uniqueIDs atomic.Int64
	var mu sync.Mutex
	var lat []time.Duration

	deadline := time.Now().Add(*dur)
	interval := time.Second * time.Duration(*workers) / time.Duration(*rps)
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(interval)
			defer t.Stop()
			for time.Now().Before(deadline) {
				<-t.C
				id := uuid.Must(uuid.NewV7()).String()
				body, _ := json.Marshal(map[string]any{
					"event_id": id, "ad_id": pick(), "user_id": fmt.Sprintf("u%d", rand.IntN(50_000)),
					"country": countries[rand.IntN(len(countries))], "device_type": devices[rand.IntN(len(devices))],
					"browser": browsers[rand.IntN(len(browsers))], "source": sources[rand.IntN(len(sources))],
				})
				n := 1
				if rand.Float64() < *dup {
					n = 2 // duplicate delivery with the same event_id
				}
				uniqueIDs.Add(1)
				for i := 0; i < n; i++ {
					s := time.Now()
					resp, err := client.Post(*url, "application/json", bytes.NewReader(body))
					d := time.Since(s)
					sent.Add(1)
					switch {
					case err != nil:
						fail.Add(1)
						continue
					case resp.StatusCode == 202:
						ok.Add(1)
					case resp.StatusCode >= 500:
						fail.Add(1)
					default:
						bad.Add(1)
					}
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					mu.Lock()
					lat = append(lat, d)
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	el := time.Since(start)

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	q := func(p float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		return lat[int(float64(len(lat)-1)*p)]
	}
	fmt.Printf("duration        %s\n", el.Round(time.Millisecond))
	fmt.Printf("requests sent   %d (%.0f/s)\n", sent.Load(), float64(sent.Load())/el.Seconds())
	fmt.Printf("202 accepted    %d\n5xx/errors      %d\n4xx             %d\n", ok.Load(), fail.Load(), bad.Load())
	fmt.Printf("distinct events %d  (expected click total once the consumer catches up, if no 5xx)\n", uniqueIDs.Load())
	fmt.Printf("latency p50=%s p95=%s p99=%s max=%s\n", q(.5), q(.95), q(.99), q(1))
}

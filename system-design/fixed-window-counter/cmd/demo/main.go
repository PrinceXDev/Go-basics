// Command demo fires requests at the API and prints what the rate limiter decided for each.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type outcome struct {
	status     int
	remaining  string
	limit      string
	retryAfter string
	err        error
}

func main() {
	url := flag.String("url", "http://localhost:8080/demo", "endpoint to call")
	n := flag.Int("n", 15, "number of requests")
	key := flag.String("key", "demo-key", "value for the X-API-Key header")
	concurrent := flag.Bool("concurrent", false, "send all requests at the same time")
	delay := flag.Duration("delay", 100*time.Millisecond, "pause between sequential requests")
	flag.Parse()

	client := &http.Client{Timeout: 5 * time.Second}
	results := make([]outcome, *n)

	call := func(i int) {
		req, _ := http.NewRequest(http.MethodGet, *url, nil)
		req.Header.Set("X-API-Key", *key)
		resp, err := client.Do(req)
		if err != nil {
			results[i] = outcome{err: err}
			return
		}
		resp.Body.Close()
		results[i] = outcome{
			status:     resp.StatusCode,
			remaining:  resp.Header.Get("X-RateLimit-Remaining"),
			limit:      resp.Header.Get("X-RateLimit-Limit"),
			retryAfter: resp.Header.Get("Retry-After"),
		}
	}

	if *concurrent {
		var wg sync.WaitGroup
		for i := 0; i < *n; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); call(i) }()
		}
		wg.Wait()
	} else {
		for i := 0; i < *n; i++ {
			call(i)
			time.Sleep(*delay)
		}
	}

	allowed, blocked := 0, 0
	for i, r := range results {
		switch {
		case r.err != nil:
			fmt.Printf("#%02d  ERROR   %v\n", i+1, r.err)
		case r.status == http.StatusOK:
			allowed++
			fmt.Printf("#%02d  %d  ALLOWED  remaining=%s limit=%s\n", i+1, r.status, r.remaining, r.limit)
		case r.status == http.StatusTooManyRequests:
			blocked++
			fmt.Printf("#%02d  %d  BLOCKED  retry-after=%ss\n", i+1, r.status, r.retryAfter)
		default:
			fmt.Printf("#%02d  %d  other\n", i+1, r.status)
		}
	}
	fmt.Printf("\nallowed=%d blocked=%d\n", allowed, blocked)
}

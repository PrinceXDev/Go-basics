package wallet

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Result struct {
	Succeeded    int
	Insufficient int
	Failed       int // anything else, e.g. ErrConflict
	Elapsed      time.Duration
}

// RunConcurrent fires one goroutine per delta and releases them all at once,
// so the operations really do overlap.
func RunConcurrent(ctx context.Context, s Strategy, id int64, deltas []int64) Result {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		res   Result
		start = make(chan struct{})
	)
	for _, d := range deltas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := s.Apply(ctx, id, d)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				res.Succeeded++
			case errors.Is(err, ErrInsufficientFunds):
				res.Insufficient++
			default:
				res.Failed++
			}
		}()
	}
	t0 := time.Now()
	close(start)
	wg.Wait()
	res.Elapsed = time.Since(t0)
	return res
}

// Repeat returns n copies of delta.
func Repeat(delta int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = delta
	}
	return out
}

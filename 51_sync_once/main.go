package main

// ============================================================================
// CONCEPT: sync.Once — run something exactly once, no matter how many
// goroutines ask for it.
//
// Classic use case: lazy-initializing a shared resource (a config, a DB
// connection pool, a singleton) the first time it's needed, safely, even
// when many goroutines reach for it at the same moment.
//
// once.Do(f) guarantees:
//   - f runs exactly once, ever, for that Once value.
//   - every caller of Do() blocks until that one run of f is FINISHED —
//     even goroutines that arrive after f has already started.
//   - it's safe to call Do() from many goroutines concurrently; no manual
//     locking needed on your part.
//
// Doing this by hand with `if !initialized { ... }` is a race condition:
// two goroutines can both see `initialized == false` and both run the
// init code. sync.Once exists specifically so you never write that bug.
//
// JS/TS comparison: closest idea is a memoized singleton factory — but in
// JS you'd never worry about two calls racing into the initializer at the
// same instant, because JS is single-threaded. In Go, without sync.Once,
// that race is real.
// ============================================================================

import (
	"fmt"
	"sync"
)

var (
	once   sync.Once
	config map[string]string
)

// loadConfig simulates an expensive one-time setup (reading a file,
// opening a connection). It must only ever actually run once.
func loadConfig() {
	fmt.Println("loading config... (this should print exactly once)")
	config = map[string]string{"env": "production"}
}

func getConfig() map[string]string {
	once.Do(loadConfig) // first caller runs loadConfig; all others just wait
	return config
}

func main() {
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cfg := getConfig() // 10 goroutines race here, loadConfig runs once
			_ = cfg
		}(i)
	}
	wg.Wait()
	fmt.Println("final config:", config)

	// ---------- A struct-scoped singleton, the common real pattern ----------
	svc := NewService()
	var wg2 sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			svc.EnsureConnected()
		}()
	}
	wg2.Wait()
	fmt.Println("connection established:", svc.connected)
}

// Service holds a resource that must be initialized exactly once,
// regardless of how many goroutines call EnsureConnected.
type Service struct {
	once      sync.Once
	connected bool
}

func NewService() *Service {
	return &Service{}
}

func (s *Service) EnsureConnected() {
	s.once.Do(func() {
		fmt.Println("connecting... (this should print exactly once)")
		s.connected = true
	})
}

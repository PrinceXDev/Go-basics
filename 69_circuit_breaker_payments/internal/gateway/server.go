// Package gateway contains a FAKE third-party payment gateway (think Stripe /
// Razorpay) plus the client our service uses to talk to it. The fake runs on
// its own port so calls go over real HTTP, and its behaviour can be switched
// at runtime to simulate outages.
package gateway

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

type Mode string

const (
	ModeHealthy Mode = "healthy" // fast, always works
	ModeDown    Mode = "down"    // every call returns 503
	ModeFlaky   Mode = "flaky"   // FailureRate% of calls return 503
	// ModeSlow charges the card but answers after SlowDelay — longer than our
	// client timeout. We see a timeout, yet the money WAS taken. This is why
	// retries must carry an idempotency key.
	ModeSlow Mode = "slow"
)

// DeclineAboveCents: amounts above this are declined (a business error).
const DeclineAboveCents = 500_00

type Settings struct {
	Mode        Mode `json:"mode"`
	FailureRate int  `json:"failureRate"` // 0-100, used by flaky
	SlowDelayMs int  `json:"slowDelayMs"`
}

type Charge struct {
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotencyKey"`
	AmountCents    int64     `json:"amountCents"`
	Status         string    `json:"status"` // succeeded | declined
	CreatedAt      time.Time `json:"createdAt"`
}

type Stats struct {
	Requests          int64 `json:"requests"`          // every HTTP call that reached us
	Rejected503       int64 `json:"rejected503"`       // calls we failed on purpose
	ChargesCaptured   int64 `json:"chargesCaptured"`   // unique successful charges
	DuplicatesBlocked int64 `json:"duplicatesBlocked"` // retries answered from the idempotency store
}

type Server struct {
	mu       sync.Mutex
	settings Settings
	charges  map[string]Charge // idempotency key -> charge
	stats    Stats
	seq      int
	run      string // per-process prefix so charge IDs stay unique across restarts
}

func NewServer() *Server {
	return &Server{
		settings: Settings{Mode: ModeHealthy, FailureRate: 50, SlowDelayMs: 2500},
		charges:  map[string]Charge{},
		run:      fmt.Sprintf("%03x", rand.IntN(0xfff)),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/charges", s.createCharge)
	mux.HandleFunc("GET /v1/charges/{key}", s.getCharge)
	mux.HandleFunc("GET /admin/settings", s.getSettings)
	mux.HandleFunc("PUT /admin/settings", s.putSettings)
	return mux
}

// shouldFail decides, based on the current mode, whether to simulate an outage.
func (s *Server) shouldFail() (fail bool, delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.Requests++
	switch s.settings.Mode {
	case ModeDown:
		s.stats.Rejected503++
		return true, 0
	case ModeFlaky:
		if rand.IntN(100) < s.settings.FailureRate {
			s.stats.Rejected503++
			return true, 0
		}
	case ModeSlow:
		return false, time.Duration(s.settings.SlowDelayMs) * time.Millisecond
	}
	return false, time.Duration(30+rand.IntN(70)) * time.Millisecond
}

func (s *Server) createCharge(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Idempotency-Key header required"})
		return
	}
	var body struct {
		AmountCents int64 `json:"amountCents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AmountCents <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid amount"})
		return
	}

	fail, delay := s.shouldFail()
	if fail {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "gateway temporarily unavailable"})
		return
	}

	// Process the charge BEFORE the (possibly long) delay, the way a real
	// gateway commits the charge and then the response gets lost or late.
	ch, replayed := s.charge(key, body.AmountCents)
	time.Sleep(delay)

	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	status := http.StatusOK
	if ch.Status == "declined" {
		status = http.StatusPaymentRequired
	}
	writeJSON(w, status, ch)
}

func (s *Server) charge(key string, amount int64) (Charge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.charges[key]; ok {
		s.stats.DuplicatesBlocked++
		return ch, true
	}
	s.seq++
	ch := Charge{
		ID:             fmt.Sprintf("ch_%s_%04d", s.run, s.seq),
		IdempotencyKey: key,
		AmountCents:    amount,
		Status:         "succeeded",
		CreatedAt:      time.Now().UTC(),
	}
	if amount > DeclineAboveCents {
		ch.Status = "declined"
	} else {
		s.stats.ChargesCaptured++
	}
	s.charges[key] = ch
	return ch, false
}

func (s *Server) getCharge(w http.ResponseWriter, r *http.Request) {
	if fail, _ := s.shouldFail(); fail {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "gateway temporarily unavailable"})
		return
	}
	s.mu.Lock()
	ch, ok := s.charges[r.PathValue("key")]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "charge not found"})
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

type AdminView struct {
	Settings Settings `json:"settings"`
	Stats    Stats    `json:"stats"`
}

func (s *Server) getSettings(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	v := AdminView{Settings: s.settings, Stats: s.stats}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	switch in.Mode {
	case ModeHealthy, ModeDown, ModeFlaky, ModeSlow:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be healthy|down|flaky|slow"})
		return
	}
	s.mu.Lock()
	s.settings.Mode = in.Mode
	if in.FailureRate > 0 && in.FailureRate <= 100 {
		s.settings.FailureRate = in.FailureRate
	}
	if in.SlowDelayMs > 0 {
		s.settings.SlowDelayMs = in.SlowDelayMs
	}
	v := AdminView{Settings: s.settings, Stats: s.stats}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

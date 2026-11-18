// Lesson 68 — API Gateway & Service-to-Service Communication (banking edition)
//
// Run:  go run ./68_api_gateway_and_s2s
//
// Starts four components on real localhost ports (httptest.NewServer opens a
// real TCP listener — the same as separate processes, minus the terminals):
//
//	customer ──HTTP──> GATEWAY ──HTTP──> PAYMENTS ──HTTP──> ACCOUNTS
//	                      │                  │
//	                      └──────HTTP────────┼──────────────> ACCOUNTS (reads)
//	                                         └──event──> NOTIFICATIONS (async)
//
// Then plays 14 scenarios an interviewer could ask about. Read README.md for
// the theory, then read the log lines below each scenario — the "rid=" value
// lets you follow one request across every service.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"
)

func main() {
	logLevel := new(slog.LevelVar) // adjustable at runtime; used to quiet step 15
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{} // shorter demo output
			}
			return a
		},
	}))

	// Two DIFFERENT keys: customer tokens vs service tokens (see auth.go).
	userKey := []byte("customer-token-key-demo-only-use-RS256-in-prod")
	svcAuth := ServiceAuth{key: []byte("service-token-key-demo-only")}

	accounts := NewAccountsService(log,
		&Account{ID: "ACC-1001", Owner: "alice", Balance: 50_000_00}, // INR 50,000.00
		&Account{ID: "ACC-2001", Owner: "bob", Balance: 10_000_00},
		&Account{ID: "ACC-3001", Owner: "carol", Balance: 0, Frozen: true},
	)
	accSrv := httptest.NewServer(accounts.Handler(svcAuth))
	defer accSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewBus()
	go RunNotifications(ctx, bus.Subscribe("transfers"), log)

	breaker := NewBreaker("accounts", 3, time.Second, log)
	payments := NewPaymentsService(&ServiceClient{
		Caller: "payments", Target: "accounts", BaseURL: accSrv.URL,
		HTTP: &http.Client{}, Auth: svcAuth, Breaker: breaker,
		MaxAttempts: 3, AttemptTimeout: 300 * time.Millisecond, Log: log,
	}, log)
	go payments.RunOutboxRelay(ctx, bus)
	paySrv := httptest.NewServer(payments.Handler(svcAuth))
	defer paySrv.Close()

	gw, err := NewGateway(GatewayConfig{
		UserKey:  userKey,
		Services: svcAuth,
		Routes: []Route{
			{Pattern: "GET /api/v1/accounts/{id}", Service: "accounts", Upstream: accSrv.URL},
			{Pattern: "POST /api/v1/transfers", Service: "payments", Upstream: paySrv.URL},
		},
		RatePerSec:   20,
		Burst:        20,
		Timeout:      2 * time.Second, // must exceed payments' retry budget (3 x 300ms + backoff)
		MaxBodyBytes: 16 << 10,
		Log:          log,
	})
	if err != nil {
		panic(err)
	}
	gwSrv := httptest.NewServer(gw)
	defer gwSrv.Close()

	c := demoClient{http: &http.Client{}}
	alice := MintUserToken(userKey, "alice", "accounts:read transfers:write", 15*time.Minute)
	carol := MintUserToken(userKey, "carol", "accounts:read", 15*time.Minute)
	transfer := func(key string, from, to string, amount int64) (int, string, http.Header) {
		return c.do("POST", gwSrv.URL+"/api/v1/transfers", alice, key, nil,
			map[string]any{"from": from, "to": to, "amount_paise": amount})
	}
	balances := func() {
		fmt.Printf("     balances: alice %s | bob %s | carol %s\n",
			inr(accounts.Balance("ACC-1001")), inr(accounts.Balance("ACC-2001")), inr(accounts.Balance("ACC-3001")))
	}

	// ---------------------------------------------------------------- EDGE
	step(1, "Public health check needs no token")
	c.show(c.do("GET", gwSrv.URL+"/healthz", "", "", nil, nil))

	step(2, "No token / forged token / expired token -> 401 at the gateway (services never see it)")
	c.show(c.do("GET", gwSrv.URL+"/api/v1/accounts/ACC-1001", "", "", nil, nil))
	forged := MintUserToken([]byte("attacker-key"), "alice", "", time.Minute)
	c.show(c.do("GET", gwSrv.URL+"/api/v1/accounts/ACC-1001", forged, "", nil, nil))
	expired := MintUserToken(userKey, "alice", "", -time.Minute)
	c.show(c.do("GET", gwSrv.URL+"/api/v1/accounts/ACC-1001", expired, "", nil, nil))

	step(3, "Alice reads HER account (200) and BOB's account (404: object-level authorization)")
	c.show(c.do("GET", gwSrv.URL+"/api/v1/accounts/ACC-1001", alice, "", nil, nil))
	c.show(c.do("GET", gwSrv.URL+"/api/v1/accounts/ACC-2001", alice, "", nil, nil))

	step(4, "Header spoofing: alice sends X-User-ID: bob -> gateway overwrites it, still 404")
	c.show(c.do("GET", gwSrv.URL+"/api/v1/accounts/ACC-2001", alice, "", map[string]string{HeaderUserID: "bob"}, nil))

	step(5, "Oversized body -> 413 before it reaches any service")
	c.show(c.do("POST", gwSrv.URL+"/api/v1/transfers", alice, "big", nil, map[string]string{"junk": strings.Repeat("x", 32<<10)}))

	// ----------------------------------------------------- ZERO TRUST INSIDE
	step(6, "Bypass the gateway and call accounts' /debit directly")
	debitURL := accSrv.URL + "/internal/accounts/ACC-1001/debit"
	body := map[string]any{"amount_paise": 100, "ref": "hack"}
	fmt.Println("     no service token:")
	c.show(c.do("POST", debitURL, "", "", nil, body))
	fmt.Println("     valid token but caller=gateway (not allowed to move money):")
	c.show(c.do("POST", debitURL, svcAuth.Mint("gateway", "accounts"), "", nil, body))
	fmt.Println("     token minted for a different audience:")
	c.show(c.do("POST", debitURL, svcAuth.Mint("payments", "notifications"), "", nil, body))

	// --------------------------------------------------------- HAPPY PATH
	step(7, "Transfer INR 500 alice -> bob: sync saga + async notification (event delivered twice!)")
	payments.DuplicateNextPublish.Store(true)
	c.show(transfer("k-001", "ACC-1001", "ACC-2001", 500_00))
	time.Sleep(100 * time.Millisecond) // let the async consumer catch up
	balances()

	// ---------------------------------------------------------- IDEMPOTENCY
	step(8, "Customer's app retries the SAME request (same Idempotency-Key) -> replayed, not re-run")
	c.show(transfer("k-001", "ACC-1001", "ACC-2001", 500_00))
	balances()
	fmt.Println("     same key, DIFFERENT amount -> rejected:")
	c.show(transfer("k-001", "ACC-1001", "ACC-2001", 900_00))
	fmt.Println("     no key at all -> rejected (money endpoints require one):")
	c.show(transfer("", "ACC-1001", "ACC-2001", 100_00))

	step(9, "Debit commits but its RESPONSE is lost -> payments retries -> accounts replays (no double debit)")
	accounts.LoseNextResponse.Store(true)
	c.show(transfer("k-002", "ACC-1001", "ACC-2001", 1_000_00))
	balances()

	// ------------------------------------------------------------ RETRIES
	step(10, "Accounts fails twice (503) -> payments retries with backoff+jitter -> success")
	accounts.FailNext.Store(2)
	c.show(transfer("k-003", "ACC-1001", "ACC-2001", 250_00))

	step(11, "Business error (insufficient funds) is NOT retried: one attempt, clear 422")
	c.show(transfer("k-004", "ACC-1001", "ACC-2001", 10_00_000_00))

	// --------------------------------------------------------------- SAGA
	step(12, "Credit to a FROZEN account fails -> saga compensates (refund) -> REVERSED")
	before := accounts.Balance("ACC-1001")
	c.show(transfer("k-005", "ACC-1001", "ACC-3001", 300_00))
	time.Sleep(100 * time.Millisecond)
	fmt.Printf("     alice before %s, after %s (unchanged = refund worked)\n", inr(before), inr(accounts.Balance("ACC-1001")))

	// ---------------------------------------------------- CIRCUIT BREAKER
	step(13, "Accounts goes DOWN -> retries exhaust -> breaker OPENS -> fail fast -> recovers")
	accounts.Down.Store(true)
	for i := 1; i <= 4; i++ {
		start := time.Now()
		status, b, _ := transfer(fmt.Sprintf("k-out-%d", i), "ACC-1001", "ACC-2001", 100_00)
		fmt.Printf("  -> attempt %d: %d in %-6v breaker=%s %s\n", i, status,
			time.Since(start).Round(time.Millisecond), breaker.State(), oneLine(b))
	}
	accounts.Down.Store(false)
	fmt.Println("     accounts is back; waiting for the 1s cooldown ...")
	time.Sleep(1100 * time.Millisecond)
	c.show(transfer("k-out-5", "ACC-1001", "ACC-2001", 100_00))
	fmt.Printf("     breaker=%s\n", breaker.State())

	step(14, "Accounts is SLOW (2.5s) -> gateway's 2s deadline -> 504, upstream work cancelled")
	accounts.Latency.Store(int64(2500 * time.Millisecond))
	c.show(c.do("GET", gwSrv.URL+"/api/v1/accounts/ACC-1001", alice, "", nil, nil))
	accounts.Latency.Store(0)

	step(15, "Carol fires 30 requests instantly -> per-customer token bucket (burst 20) -> 429s")
	counts := map[int]int{}
	logLevel.Set(slog.LevelWarn) // 30 access-log lines would bury the result
	for range 30 {
		s, _, _ := c.do("GET", gwSrv.URL+"/api/v1/accounts/ACC-3001", carol, "", nil, nil)
		counts[s]++
	}
	logLevel.Set(slog.LevelInfo)
	fmt.Printf("  -> 200 OK: %d, 429 Too Many Requests: %d (alice is unaffected: separate bucket)\n",
		counts[200], counts[429])

	fmt.Println()
	balances()
	fmt.Println("\nDone. Now read README.md section by section and match each concept to a step above.")
}

// ---------------------------------------------------------------------------
// demo helpers
// ---------------------------------------------------------------------------

type demoClient struct{ http *http.Client }

func (d demoClient) do(method, url, token, idemKey string, hdr map[string]string, body any) (int, string, http.Header) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idemKey != "" {
		req.Header.Set(HeaderIdemKey, idemKey)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return 0, err.Error(), nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func (d demoClient) show(status int, body string, h http.Header) {
	extra := ""
	if h.Get(HeaderReplay) == "true" {
		extra = "  [Idempotent-Replay: true]"
	}
	fmt.Printf("  -> %d %s%s\n", status, oneLine(body), extra)
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 170 {
		s = s[:170] + "..."
	}
	return s
}

func step(n int, title string) {
	fmt.Printf("\n=== %d. %s\n", n, title)
}

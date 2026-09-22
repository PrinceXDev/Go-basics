// Command demo runs all three services IN ONE PROCESS (three real listeners,
// real gRPC over localhost, real HTTP) and drives a scripted scenario through
// them, including a simulated outage of the user service.
//
// This exists so you can watch the whole system behave with one command.
// The services themselves are the same code the three separate binaries run;
// see README.md for running them as genuinely separate processes, which is
// what you should do at least once.
//
// Run: go run ./63_microservices/cmd/demo
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	orderv1 "example.com/go-basics/63_microservices/gen/order/v1"
	userv1 "example.com/go-basics/63_microservices/gen/user/v1"
	"example.com/go-basics/63_microservices/internal/gateway"
	"example.com/go-basics/63_microservices/internal/orderservice"
	"example.com/go-basics/63_microservices/internal/platform"
	"example.com/go-basics/63_microservices/internal/userservice"
)

const (
	userAddr  = "127.0.0.1:50051"
	orderAddr = "127.0.0.1:50052"
	httpAddr  = "127.0.0.1:18080"
	baseURL   = "http://" + httpAddr
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	banner()

	// ---- user service -------------------------------------------------
	userSvc := userservice.New() // kept so the demo can inject failures
	userLog := platform.NewLogger("userd", false)
	userSrv := platform.NewServer(userAddr, userLog)
	userv1.RegisterUserServiceServer(userSrv.GRPC, userSvc)
	go func() { _ = userSrv.Run(ctx, "user.v1.UserService") }()

	// ---- order service (depends on user) --------------------------------
	orderLog := platform.NewLogger("orderd", false)
	userConn, err := platform.Dial(userAddr)
	exitOn(err)
	defer userConn.Close()

	orderSvc := orderservice.New(userv1.NewUserServiceClient(userConn),
		func(name string, from, to platform.BreakerState) {
			fmt.Printf("\n   *** CIRCUIT BREAKER [%s]: %s -> %s ***\n\n", name, from, to)
		})

	orderSrv := platform.NewServer(orderAddr, orderLog)
	orderv1.RegisterOrderServiceServer(orderSrv.GRPC, orderSvc)
	go func() { _ = orderSrv.Run(ctx, "order.v1.OrderService") }()

	// ---- gateway --------------------------------------------------------
	orderConn, err := platform.Dial(orderAddr)
	exitOn(err)
	defer orderConn.Close()

	gw := gateway.New(userv1.NewUserServiceClient(userConn), orderv1.NewOrderServiceClient(orderConn),
		platform.NewLogger("gateway", false))
	go func() { _ = gw.Serve(ctx, httpAddr) }()

	waitForHealthy()

	// =====================================================================
	step("1. Happy path: fetch a user through the gateway")
	get("/users/u1")

	step("2. Create an order -> gateway -> order svc -> user svc (credit check)")
	post("/orders", `{"user_id":"u1","lines":[{"sku":"XSHOT","quantity":2,"unit_price_cents":2999}]}`, "")
	fmt.Println("   watch the logs: ONE request_id appears in gateway, orderd AND userd")

	step("3. Business rejection: not enough credit -> 422, not 500")
	post("/orders", `{"user_id":"u2","lines":[{"sku":"DRILL","quantity":9,"unit_price_cents":8999}]}`, "")

	step("4. Inactive account -> FailedPrecondition -> 422")
	post("/orders", `{"user_id":"u3","lines":[{"sku":"XSHOT","quantity":1,"unit_price_cents":2999}]}`, "")

	step("5. Unknown user -> NotFound propagates as 404")
	post("/orders", `{"user_id":"ghost","lines":[{"sku":"XSHOT","quantity":1,"unit_price_cents":2999}]}`, "")

	step("6. Validation at the edge -> 400 without ever leaving the gateway")
	post("/orders", `{"user_id":"u1","lines":[]}`, "")

	step("7. IDEMPOTENCY: the same key twice creates ONE order")
	body := `{"user_id":"u1","lines":[{"sku":"ROBO","quantity":1,"unit_price_cents":4999}]}`
	post("/orders", body, "key-abc-123")
	post("/orders", body, "key-abc-123")
	fmt.Println("   same order id both times -> a retry cannot double-charge")

	// =====================================================================
	step("8. OUTAGE. The user service goes down for 4 seconds.")
	userSvc.FailFor(4 * time.Second)
	fmt.Println("   small order -> retries, then DEGRADED MODE (accepted as PENDING)")
	post("/orders", `{"user_id":"u1","lines":[{"sku":"CHEAP","quantity":1,"unit_price_cents":500}]}`, "")

	step("9. Same outage, HIGH VALUE order -> fail closed with 503")
	slow := time.Now()
	post("/orders", `{"user_id":"u1","lines":[{"sku":"BIG","quantity":1,"unit_price_cents":45000}]}`, "")
	slowTook := time.Since(slow)
	fmt.Printf("   took %v (3 attempts + backoff), breaker: %s\n", slowTook.Round(time.Millisecond), orderSvc.BreakerState())

	step("10. Keep failing until the breaker TRIPS (3 consecutive failures)")
	for orderSvc.BreakerState() == platform.Closed {
		post("/orders", `{"user_id":"u1","lines":[{"sku":"BIG2","quantity":1,"unit_price_cents":45000}]}`, "")
	}
	fmt.Printf("   breaker is now: %s\n", orderSvc.BreakerState())

	step("11. Breaker OPEN -> the next call fails INSTANTLY, without touching the sick service")
	t0 := time.Now()
	post("/orders", `{"user_id":"u1","lines":[{"sku":"BIG3","quantity":1,"unit_price_cents":45000}]}`, "")
	fmt.Printf("   took %v  vs  %v in step 9 -- and userd logged NOTHING, because\n",
		time.Since(t0).Round(time.Millisecond), slowTook.Round(time.Millisecond))
	fmt.Println("   no call was made at all. That is the entire point of the breaker:")
	fmt.Println("   you stop spending your own goroutines and your dependency's CPU")
	fmt.Println("   on a request you already know will fail.")

	step("12. RECOVERY. The user service comes back; after the cooldown one probe closes the circuit.")
	userSvc.Recover()
	time.Sleep(2100 * time.Millisecond) // wait out the 2s cooldown
	post("/orders", `{"user_id":"u1","lines":[{"sku":"BACK","quantity":1,"unit_price_cents":1500}]}`, "")
	fmt.Printf("   breaker is now: %s\n", orderSvc.BreakerState())

	// =====================================================================
	step("13. SLOW dependency -> the per-attempt deadline fires -> 504")
	userSvc.SetLatency(900 * time.Millisecond) // > the 300ms attempt budget
	post("/orders", `{"user_id":"u1","lines":[{"sku":"SLOW","quantity":1,"unit_price_cents":1000}]}`, "")
	userSvc.SetLatency(0)
	fmt.Println("   note: DeadlineExceeded is NOT retried -- retrying a timeout")
	fmt.Println("   steals budget from the caller and amplifies the overload")

	step("14. FAN-OUT: one HTTP call, two services queried CONCURRENTLY (errgroup)")
	get("/users/u1/dashboard")

	// =====================================================================
	step("15. GRACEFUL SHUTDOWN of all three")
	cancel()
	time.Sleep(700 * time.Millisecond)

	fmt.Print(`
===========================================================================
WHAT YOU JUST WATCHED, AND WHY EACH PIECE EXISTS

  request id           one click, three processes, one searchable id
  deadline budget      300ms per attempt < 3s gateway budget < client's
  retry + jitter       survives a blip; jitter avoids a thundering herd
  retryable codes only Unavailable yes, DeadlineExceeded/InvalidArgument no
  circuit breaker      after 3 failures, fail in microseconds not seconds
  degraded mode        a business decision: small orders PENDING, big ones 503
  idempotency key      the reason retrying a write is safe at all
  code translation     gRPC codes -> HTTP codes, exactly once, at the edge
  errgroup fan-out     parallel calls, first error cancels the rest
  graceful shutdown    NOT_SERVING -> drain -> stop, so deploys drop nothing

THE INTERVIEW QUESTIONS THIS ANSWERS

  "How do services find each other?"
      A resolver in the target string. dns:///svc:50051 + round_robin gives
      client-side load balancing with no sidecar. Kubernetes headless
      Services, Consul and etcd all plug in the same way.

  "How do you stop a cascading failure?"
      Bound everything (deadlines), fail fast when a dependency is clearly
      down (breaker), never let retries multiply across layers, and decide
      per-endpoint whether to fail open or closed.

  "How do you debug a slow request across five services?"
      Propagate a request/trace id from the edge and log it everywhere.
      Then upgrade to OpenTelemetry spans for the timing waterfall.

  "When would you NOT use microservices?"
      Almost always at the start. You pay in latency, partial failure,
      operational complexity, and distributed transactions. Split a monolith
      when teams -- not modules -- start blocking each other. A well-layered
      monolith with clean package boundaries beats a bad distributed system
      every single time, and it is far easier to split later than to merge.
===========================================================================
`)
}

// ---------------------------------------------------------------------------
// tiny HTTP helpers
// ---------------------------------------------------------------------------

var client = &http.Client{Timeout: 10 * time.Second}

func get(path string) {
	req, _ := http.NewRequest(http.MethodGet, baseURL+path, nil)
	do(req)
}

func post(path, body, idemKey string) {
	req, _ := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	do(req)
}

func do(req *http.Request) {
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("   request failed:", err)
		return
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var pretty bytes.Buffer
	if json.Indent(&pretty, raw, "   ", "  ") != nil {
		pretty.Write(raw)
	}
	fmt.Printf("   HTTP %d %s\n   %s\n", resp.StatusCode, req.Method+" "+req.URL.Path, strings.TrimSpace(pretty.String()))
}

func waitForHealthy() {
	for i := 0; i < 50; i++ {
		resp, err := client.Get(baseURL + "/healthz")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Println("gateway never became healthy")
	os.Exit(1)
}

func step(s string) {
	fmt.Printf("\n\n===== %s =====\n", s)
	time.Sleep(60 * time.Millisecond) // let the async log lines land first
}

func banner() {
	fmt.Print(`
===========================================================================
 THREE SERVICES, ONE REQUEST PATH

   browser/curl
        |  HTTP + JSON
        v
   +----------------+        gRPC         +-----------------+
   |    gateway     | ------------------> |  order service  |
   |  (Gin, :18080) |                     |     (:50052)    |
   +----------------+                     +-----------------+
        |  gRPC                                    | gRPC
        |                                          | + retry
        |                                          | + circuit breaker
        |                                          | + 300ms deadline
        +--------------------> +-----------------+ |
                               |  user service   | <
                               |    (:50051)     |
                               +-----------------+

 Each box owns its own data. Nothing shares a database.
===========================================================================
`)
}

func exitOn(err error) {
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

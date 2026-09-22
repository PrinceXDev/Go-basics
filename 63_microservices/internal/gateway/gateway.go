// Package gateway is the EDGE. It is the only process browsers talk to.
//
// WHY YOU NEED ONE:
//   - browsers cannot speak gRPC (no HTTP/2 trailer access in fetch)
//   - the outside world wants JSON, REST-ish URLs and HTTP status codes
//   - auth, rate limiting, CORS and request-id minting belong in ONE place,
//     not duplicated in every internal service
//   - internal services stay private; only this port is exposed
//
// What it must NOT become: a place where business logic accumulates. The
// gateway translates and routes. The moment it starts making decisions
// about orders, you have a distributed monolith with extra latency.
package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	orderv1 "example.com/go-basics/63_microservices/gen/order/v1"
	userv1 "example.com/go-basics/63_microservices/gen/user/v1"
	"example.com/go-basics/63_microservices/internal/platform"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Gateway struct {
	users  userv1.UserServiceClient
	orders orderv1.OrderServiceClient
	log    *slog.Logger
}

func New(users userv1.UserServiceClient, orders orderv1.OrderServiceClient, log *slog.Logger) *Gateway {
	return &Gateway{users: users, orders: orders, log: log}
}

func (g *Gateway) Router() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	r.Use(g.requestIDMiddleware(), g.loggingMiddleware(), gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })

	r.GET("/users/:id", g.getUser)
	r.POST("/orders", g.createOrder)
	r.GET("/orders/:id", g.getOrder)
	r.GET("/users/:id/dashboard", g.dashboard)

	return r
}

// ---------------------------------------------------------------------------
// MIDDLEWARE
// ---------------------------------------------------------------------------

// requestIDMiddleware mints the id ONCE, here at the edge, and seeds the
// context. platform.ForwardRequestIDUnary then carries it to every service
// this request touches. Accepting a client-supplied id is convenient for
// debugging but means clients can forge it -- fine internally, think twice
// on a public edge.
func (g *Gateway) requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			id = platform.NewRequestID()
		}
		ctx := platform.WithRequestID(c.Request.Context(), id)
		ctx = platform.WithLogger(ctx, g.log.With("request_id", id))

		c.Request = c.Request.WithContext(ctx)
		c.Writer.Header().Set("X-Request-ID", id) // so the client can quote it in a bug report
		c.Next()
	}
}

func (g *Gateway) loggingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		platform.Logger(c.Request.Context()).Info("http",
			"method", c.Request.Method, "path", c.FullPath(),
			"status", c.Writer.Status(), "duration_ms", time.Since(start).Milliseconds())
	}
}

// ---------------------------------------------------------------------------
// THE TRANSLATION LAYER: gRPC status code -> HTTP status code.
//
// This mapping is the gateway's single most important job. Get it wrong and
// every client's retry logic misbehaves: a 500 tells a browser to retry
// something that will never work, a 400 hides a real outage from your alerts.
// ---------------------------------------------------------------------------

func httpStatus(code codes.Code) int {
	switch code {
	case codes.OK:
		return http.StatusOK
	case codes.InvalidArgument, codes.OutOfRange:
		return http.StatusBadRequest // 400
	case codes.Unauthenticated:
		return http.StatusUnauthorized // 401
	case codes.PermissionDenied:
		return http.StatusForbidden // 403
	case codes.NotFound:
		return http.StatusNotFound // 404
	case codes.AlreadyExists, codes.Aborted:
		return http.StatusConflict // 409
	case codes.FailedPrecondition:
		return http.StatusUnprocessableEntity // 422
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests // 429
	case codes.Canceled:
		return 499 // nginx's "client closed request"
	case codes.Unimplemented:
		return http.StatusNotImplemented // 501
	case codes.Unavailable:
		return http.StatusServiceUnavailable // 503
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout // 504
	default:
		return http.StatusInternalServerError // 500
	}
}

func (g *Gateway) fail(c *gin.Context, err error) {
	st := status.Convert(err)
	code := httpStatus(st.Code())

	// Never forward an internal error message to the public. It leaks
	// table names, hostnames and stack context. Log it, return a generic one.
	msg := st.Message()
	if code >= 500 {
		platform.Logger(c.Request.Context()).Error("upstream failure", "grpc_code", st.Code().String(), "error", err)
		msg = "upstream service error"
	}

	c.JSON(code, gin.H{
		"error":      msg,
		"code":       st.Code().String(),
		"request_id": platform.RequestID(c.Request.Context()),
	})
}

// ---------------------------------------------------------------------------
// HANDLERS
// ---------------------------------------------------------------------------

func (g *Gateway) getUser(c *gin.Context) {
	// EVERY outbound call gets a deadline, derived from the request context
	// so a client disconnect cancels the work immediately.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	resp, err := g.users.GetUser(ctx, &userv1.GetUserRequest{Id: c.Param("id")})
	if err != nil {
		g.fail(c, err)
		return
	}
	u := resp.GetUser()
	// A hand-written DTO, not the protobuf struct. Two reasons: protobuf
	// JSON uses different casing rules, and more importantly you do not want
	// an internal field to leak into the public API the day someone adds it
	// to the proto.
	c.JSON(http.StatusOK, gin.H{
		"id": u.GetId(), "email": u.GetEmail(), "name": u.GetFullName(),
		"credit_limit_cents": u.GetCreditLimitCents(), "active": u.GetActive(),
	})
}

type createOrderBody struct {
	UserID string `json:"user_id" binding:"required"`
	Lines  []struct {
		SKU            string `json:"sku" binding:"required"`
		Quantity       int32  `json:"quantity" binding:"required,gt=0"`
		UnitPriceCents int64  `json:"unit_price_cents" binding:"required,gt=0"`
	} `json:"lines" binding:"required,min=1,dive"`
}

func (g *Gateway) createOrder(c *gin.Context) {
	var body createOrderBody
	// Validate at the EDGE. Rejecting garbage here saves a network hop and
	// keeps the internal service's error rate meaningful.
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "code": "INVALID_ARGUMENT"})
		return
	}

	lines := make([]*orderv1.OrderLine, 0, len(body.Lines))
	for _, l := range body.Lines {
		lines = append(lines, &orderv1.OrderLine{Sku: l.SKU, Quantity: l.Quantity, UnitPriceCents: l.UnitPriceCents})
	}

	// The client may supply its own idempotency key (the correct pattern for
	// payments). Otherwise the request id doubles as one.
	key := c.GetHeader("Idempotency-Key")
	if key == "" {
		key = platform.RequestID(c.Request.Context())
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	resp, err := g.orders.CreateOrder(ctx, &orderv1.CreateOrderRequest{
		UserId: body.UserID, Lines: lines, IdempotencyKey: key,
	})
	if err != nil {
		g.fail(c, err)
		return
	}

	o := resp.GetOrder()
	c.JSON(http.StatusCreated, gin.H{
		"id": o.GetId(), "user_id": o.GetUserId(),
		"total_cents": o.GetTotalCents(), "status": o.GetStatus().String(),
	})
}

func (g *Gateway) getOrder(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	resp, err := g.orders.GetOrder(ctx, &orderv1.GetOrderRequest{Id: c.Param("id")})
	if err != nil {
		g.fail(c, err)
		return
	}
	o := resp.GetOrder()
	c.JSON(http.StatusOK, gin.H{
		"id": o.GetId(), "user_id": o.GetUserId(),
		"total_cents": o.GetTotalCents(), "status": o.GetStatus().String(),
	})
}

// ---------------------------------------------------------------------------
// FAN-OUT: one HTTP request, two services called CONCURRENTLY.
//
// This is where Go's concurrency pays for the whole architecture. Sequential
// would be 2 x latency; errgroup makes it max(latency). And errgroup.WithContext
// cancels the sibling call the instant one fails -- no wasted work.
//
// errgroup vs sync.WaitGroup: WaitGroup waits. errgroup waits, collects the
// FIRST error, and cancels a derived context. Use errgroup whenever the
// parallel work can fail.
// ---------------------------------------------------------------------------

func (g *Gateway) dashboard(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	userID := c.Param("id")

	var (
		user   *userv1.User
		orders []*orderv1.Order
	)

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		resp, err := g.users.GetUser(ctx, &userv1.GetUserRequest{Id: userID})
		if err != nil {
			return err
		}
		user = resp.GetUser()
		return nil
	})

	eg.Go(func() error {
		resp, err := g.orders.ListOrders(ctx, &orderv1.ListOrdersRequest{UserId: userID})
		if err != nil {
			// PARTIAL FAILURE POLICY: the order list is "nice to have" here.
			// Swallow its failure and render the page without it rather than
			// 500-ing the whole dashboard. Decide this per field, explicitly.
			platform.Logger(ctx).Warn("orders unavailable, degrading", "error", err)
			return nil
		}
		orders = resp.GetOrders()
		return nil
	})

	if err := eg.Wait(); err != nil {
		g.fail(c, err)
		return
	}

	out := make([]gin.H, 0, len(orders))
	for _, o := range orders {
		out = append(out, gin.H{"id": o.GetId(), "total_cents": o.GetTotalCents(), "status": o.GetStatus().String()})
	}
	c.JSON(http.StatusOK, gin.H{
		"user":   gin.H{"id": user.GetId(), "name": user.GetFullName()},
		"orders": out,
	})
}

// Serve runs the HTTP server with the same graceful-shutdown discipline as
// the gRPC services (lesson 42).
func (g *Gateway) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: g.Router(),
		// These four are not optional on a public listener. Without them a
		// single slow client can hold a connection (and its goroutine) open
		// forever -- the Slowloris attack.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		g.log.Info("gateway listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	g.log.Info("gateway draining")
	return srv.Shutdown(shutdownCtx)
}

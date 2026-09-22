// Package platform holds everything that is NOT business logic: the wiring
// every service in the fleet shares -- request IDs, logging, panic recovery,
// retries, circuit breaking, health and graceful shutdown.
//
// In a real company this is a separate internal module (github.com/acme/go-kit)
// that every service imports. Keeping it in one place is what stops twelve
// teams from inventing twelve subtly different retry policies.
package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ===========================================================================
// REQUEST ID PROPAGATION
//
// THE PROBLEM: one user click becomes gateway -> order -> user, three
// processes, three log streams. When it breaks at 3am you need to find all
// the lines belonging to THAT click.
//
// THE FIX: generate an id at the edge, put it in the context, and forward it
// on every hop. Every log line carries it. This is the poor man's distributed
// tracing -- and it is 90% of the value of OpenTelemetry for 2% of the setup.
// (Real tracing adds spans and timings; the propagation idea is identical.)
// ===========================================================================

const RequestIDKey = "x-request-id"

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyLogger
)

func NewRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// WithLogger / Logger carry a request-scoped logger. Every log line from
// anywhere in the call then automatically has the request id attached,
// without threading a logger parameter through every function signature.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKeyLogger, l)
}

func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKeyLogger).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

func NewLogger(service string, jsonOut bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var h slog.Handler = slog.NewTextHandler(os.Stdout, opts)
	if jsonOut {
		// JSON in production: your log aggregator can index the fields.
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h).With("service", service)
}

// ===========================================================================
// SERVER INTERCEPTORS
// ===========================================================================

// UnaryServerChain is the standard chain every service in this fleet runs.
// Order: recovery (outermost) -> request id -> logging -> handler.
func UnaryServerChain(log *slog.Logger) grpc.ServerOption {
	return grpc.ChainUnaryInterceptor(
		recoveryUnary(log),
		requestIDUnary(log),
		loggingUnary(),
	)
}

func recoveryUnary(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic recovered",
					"method", info.FullMethod, "panic", r,
					"stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

// requestIDUnary reads the id from incoming metadata, or mints one if this
// is the first hop, then puts both the id and a pre-tagged logger on the ctx.
func requestIDUnary(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v := md.Get(RequestIDKey); len(v) > 0 {
				id = v[0]
			}
		}
		if id == "" {
			id = NewRequestID()
		}

		ctx = WithRequestID(ctx, id)
		ctx = WithLogger(ctx, log.With("request_id", id))

		// Echo it back so the CALLER can log it too, even on failure.
		_ = grpc.SetHeader(ctx, metadata.Pairs(RequestIDKey, id))
		return handler(ctx, req)
	}
}

func loggingUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)

		code := status.Code(err)
		l := Logger(ctx).With(
			"method", info.FullMethod,
			"code", code.String(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
		// Log level follows the code: a NotFound is not an incident, an
		// Internal is. Getting this wrong is how alert fatigue starts.
		switch code {
		case codes.OK:
			l.Info("rpc")
		case codes.Internal, codes.Unknown, codes.DataLoss:
			l.Error("rpc failed", "error", err)
		default:
			l.Warn("rpc rejected", "error", err)
		}
		return resp, err
	}
}

// ===========================================================================
// CLIENT INTERCEPTOR -- forward the request id to the next hop.
// Without this, the chain breaks at the first service boundary.
// ===========================================================================

func ForwardRequestIDUnary() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if id := RequestID(ctx); id != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, RequestIDKey, id)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

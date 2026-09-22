package main

// ===========================================================================
// INTERCEPTORS -- gRPC's middleware.
//
// Two flavours, because unary and streaming RPCs have different shapes:
//
//	UnaryServerInterceptor(ctx, req, info, handler) (resp, err)
//	StreamServerInterceptor(srv, ss, info, handler) error
//
// And the mirror image on the client side. If you write an interceptor, you
// almost always need BOTH server variants, or streaming RPCs quietly skip
// your logging/auth/metrics.
//
// ORDERING: grpc.ChainUnaryInterceptor(a, b, c) runs a -> b -> c -> handler
// on the way in, and unwinds c -> b -> a on the way out. So:
//
//	Recovery FIRST  (outermost) -- it must catch panics from everything inside
//	Logging next    -- so it observes the recovered error and the true duration
//	Auth last       -- cheapest to reject, and it needs nothing above it
//
// This is exactly the same reasoning as the net/http middleware chain in
// lesson 39; only the function signature changed.
// ===========================================================================

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// RECOVERY -- a panic in one handler must not kill the whole server process.
//
// gRPC does NOT recover panics for you (unlike net/http, which kills only the
// connection). Without this interceptor, one nil-map write takes down every
// in-flight RPC on the box.
// ---------------------------------------------------------------------------

func recoveryUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			// Log the stack for YOU. Return an opaque Internal to the CALLER --
			// never leak a stack trace across a service boundary.
			log.Printf("   [panic] %s: %v\n%s", info.FullMethod, r, firstLines(string(debug.Stack()), 3))
			err = status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(ctx, req)
}

func recoveryStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("   [panic] %s: %v", info.FullMethod, r)
			err = status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(srv, ss)
}

// ---------------------------------------------------------------------------
// LOGGING -- method, duration, status code. The three fields you actually
// page on. status.Code(err) is the correct way to extract the code: it
// returns codes.OK for a nil error and codes.Unknown for a non-status error.
// ---------------------------------------------------------------------------

func loggingUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	log.Printf("   [grpc] %-46s %-16s %v", info.FullMethod, status.Code(err), time.Since(start).Round(time.Millisecond))
	return resp, err
}

func loggingStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	start := time.Now()
	err := handler(srv, ss)
	log.Printf("   [grpc] %-46s %-16s %v (stream)", info.FullMethod, status.Code(err), time.Since(start).Round(time.Millisecond))
	return err
}

// ---------------------------------------------------------------------------
// AUTH -- gRPC's equivalent of an HTTP header is METADATA.
//
// metadata.MD is a map[string][]string carried in HTTP/2 headers. Keys are
// lowercased automatically. Keys ending in "-bin" carry binary values.
//
// Note there is no Authorization "header" type -- it is just a metadata key
// by convention, and the same "authorization: Bearer <token>" string as REST.
// ---------------------------------------------------------------------------

// Methods anyone may call without a token.
var publicMethods = map[string]bool{
	"/grpc.health.v1.Health/Check": true,
}

func authUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := authorize(ctx, info.FullMethod); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func authStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := authorize(ss.Context(), info.FullMethod); err != nil {
		return err
	}
	return handler(srv, ss)
}

func authorize(ctx context.Context, method string) error {
	if publicMethods[method] {
		return nil
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "no metadata")
	}

	// md.Get is case-insensitive and returns a slice -- a metadata key can
	// legitimately appear more than once.
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return status.Error(codes.Unauthenticated, "missing authorization metadata")
	}

	token, found := strings.CutPrefix(vals[0], "Bearer ")
	if !found || token != "s3rvice-t0ken" {
		// Unauthenticated = "I do not know who you are" (401).
		// PermissionDenied = "I know you, you may not do this" (403).
		// Same distinction as lesson 60; getting it wrong breaks retry logic
		// in every client library.
		return status.Error(codes.Unauthenticated, "invalid token")
	}
	return nil
}

// ---------------------------------------------------------------------------
// CLIENT-SIDE INTERCEPTOR -- attach credentials and a request id to every
// outgoing call, so no individual call site can forget.
//
// In a microservice mesh this is where request-id / trace-id propagation
// lives (lesson 63 does exactly that across three processes).
// ---------------------------------------------------------------------------

func clientAuthUnary(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	ctx = metadata.AppendToOutgoingContext(ctx,
		"authorization", "Bearer s3rvice-t0ken",
		"x-request-id", fmt.Sprintf("req-%d", time.Now().UnixNano()%100000),
	)
	return invoker(ctx, method, req, reply, cc, opts...)
}

func clientAuthStream(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer s3rvice-t0ken")
	return streamer(ctx, desc, cc, method, opts...)
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

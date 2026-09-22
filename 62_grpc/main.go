package main

// ============================================================================
// CONCEPT: gRPC + Protocol Buffers -- how Go services talk to each other.
//
// WHY NOT JUST REST+JSON?
//
//	                REST + JSON              gRPC + protobuf
//	contract        OpenAPI, if you keep     the .proto file, enforced by
//	                it up to date (nobody     the compiler on BOTH sides
//	                does)
//	payload         text, ~2-5x bigger       binary, field tags not names
//	transport       HTTP/1.1, one req at     HTTP/2, multiplexed streams
//	                a time per connection     over ONE connection
//	streaming       SSE / WebSocket bolted   first-class, four shapes
//	                on the side
//	codegen         optional, bolted on      mandatory, both sides
//	browser         native                   needs grpc-web / a gateway
//	debugging       curl                     grpcurl (one extra tool)
//
// The honest summary: REST for public/browser APIs, gRPC for
// service-to-service inside your own network. Most companies run both, with
// an HTTP gateway at the edge -- which is exactly what lesson 63 builds.
//
// THE WIRE, ONCE:
//   Each field is encoded as (tag << 3 | wire_type) then the value. Field
//   NAMES never travel. Default/zero values are not sent at all -- which is
//   why proto3 cannot distinguish "0" from "unset" unless you mark a field
//   `optional` (which generates a pointer in Go).
//
// Run:        go run ./62_grpc
// Regenerate: cd 62_grpc && buf lint && buf generate
// ============================================================================

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	inventoryv1 "example.com/go-basics/62_grpc/gen/inventory/v1"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

func main() {
	log.SetFlags(0)

	// =====================================================================
	// THE SERVER
	// =====================================================================
	lis, err := net.Listen("tcp", "127.0.0.1:0") // :0 = let the OS pick a free port
	if err != nil {
		log.Fatal(err)
	}
	addr := lis.Addr().String()

	srv := grpc.NewServer(
		// Interceptor chains. Order matters -- see interceptors.go.
		grpc.ChainUnaryInterceptor(recoveryUnary, loggingUnary, authUnary),
		grpc.ChainStreamInterceptor(recoveryStream, loggingStream, authStream),

		// Limits. The defaults are 4MB receive / unlimited send. Set them
		// explicitly: an unbounded message size is a memory-exhaustion DoS.
		grpc.MaxRecvMsgSize(4*1024*1024),

		// Keepalive pings detect a peer that vanished (NAT timeout, hard
		// crash) instead of leaving a half-open connection forever.
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second, // ping an idle connection every 30s
			Timeout: 10 * time.Second, // no pong in 10s -> drop it
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             15 * time.Second, // clients pinging faster get disconnected
			PermitWithoutStream: true,
		}),
	)

	inventoryv1.RegisterInventoryServiceServer(srv, newInventoryServer())

	go func() {
		if err := srv.Serve(lis); err != nil {
			log.Printf("server stopped: %v", err)
		}
	}()
	// GracefulStop stops accepting new RPCs and waits for in-flight ones to
	// finish -- the gRPC equivalent of http.Server.Shutdown (lesson 42).
	defer srv.GracefulStop()

	fmt.Printf("inventory service listening on %s\n\n", addr)

	// =====================================================================
	// THE CLIENT
	//
	// grpc.NewClient replaces the old grpc.Dial. It does NOT connect
	// eagerly: the connection is established lazily on the first RPC, and
	// re-established automatically after a failure. So "dial succeeded"
	// tells you nothing about reachability -- your health check does.
	// =====================================================================
	conn, err := grpc.NewClient(addr,
		// insecure ONLY because this is localhost. In production this is
		// credentials.NewTLS(...) and, between services, mTLS.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(clientAuthUnary),
		grpc.WithChainStreamInterceptor(clientAuthStream),

		// Built-in retry policy, expressed as service config JSON. gRPC
		// retries ONLY the listed codes, and only when it is safe (the
		// request was never delivered, or the server said so). Note what is
		// NOT in the list: DeadlineExceeded and Internal. Retrying those
		// turns one slow request into an accidental DDoS on yourself.
		grpc.WithDefaultServiceConfig(`{
			"methodConfig": [{
				"name": [{"service": "inventory.v1.InventoryService"}],
				"retryPolicy": {
					"MaxAttempts": 3,
					"InitialBackoff": "0.1s",
					"MaxBackoff": "1s",
					"BackoffMultiplier": 2.0,
					"RetryableStatusCodes": ["UNAVAILABLE", "RESOURCE_EXHAUSTED"]
				}
			}]
		}`),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	client := inventoryv1.NewInventoryServiceClient(conn)
	ctx := context.Background()

	// =====================================================================
	demo1Unary(ctx, client)
	demo2Errors(ctx, client)
	demo3Deadline(ctx, client)
	demo4Cancel(ctx, client)
	demo5Panic(ctx, client)
	demo6NoAuth(addr)
	demo7ServerStream(ctx, client)
	demo8ClientStream(ctx, client)
	demo9BidiStream(ctx, client)

	fmt.Print(`
--- STATUS CODES YOU MUST KNOW COLD ----------------------------------------

  OK                  success
  InvalidArgument     caller sent garbage        -> never retry (400)
  NotFound            no such entity             -> never retry (404)
  AlreadyExists       unique violation           -> never retry (409)
  PermissionDenied    known caller, not allowed  -> never retry (403)
  Unauthenticated     unknown caller             -> re-auth, then retry (401)
  ResourceExhausted   rate limited / quota       -> retry WITH backoff (429)
  FailedPrecondition  system state wrong         -> fix state, then retry
  Aborted             concurrency conflict       -> retry the whole txn
  Unavailable         server down / no route     -> retry WITH backoff (503)
  DeadlineExceeded    ran out of time            -> do NOT blind-retry (504)
  Internal            a bug on the server        -> do NOT retry, page someone
  Unimplemented       method not on this server  -> version mismatch

The retryability column IS the reason to pick the right code. A client
library reads the code, not your message.

--- THE FIVE MISTAKES I SEE MOST OFTEN -------------------------------------

1. Returning a bare Go error from a handler. It becomes codes.Unknown, and
   every caller treats it as a mystery. Always status.Error(code, msg).
2. No deadline on the client. gRPC has NO default timeout -- a call can hang
   forever. Every outbound call gets a context.WithTimeout. Every one.
3. Reusing field tags after deleting a field. Use "reserved". Old clients
   will happily decode the new field into the old meaning.
4. Creating a ClientConn per request. It is expensive and long-lived; make
   ONE per target, share it, it is goroutine-safe and multiplexes internally.
5. Forgetting the stream interceptor twin, so streaming RPCs bypass auth.

--- HOW YOU DEBUG THIS IN PRODUCTION ---------------------------------------

  grpcurl -plaintext localhost:50051 list             # needs reflection on
  grpcurl -plaintext -d '{"id":"p1"}' localhost:50051 inventory.v1.InventoryService/GetProduct
  GRPC_GO_LOG_SEVERITY_LEVEL=info GRPC_GO_LOG_VERBOSITY_LEVEL=2 go run .

  Register reflection in dev (not prod -- it exposes your whole API):
    reflection.Register(srv)
`)
}

// ===========================================================================

func demo1Unary(ctx context.Context, c inventoryv1.InventoryServiceClient) {
	fmt.Println("=== 1. Unary call ===")

	// EVERY outbound RPC gets a deadline. No exceptions.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	resp, err := c.GetProduct(ctx, &inventoryv1.GetProductRequest{Id: "p1"})
	if err != nil {
		log.Println("  unexpected:", err)
		return
	}
	p := resp.GetProduct()
	fmt.Printf("  %s | %s | %d cents | stock %d | %s\n",
		p.GetId(), p.GetName(), p.GetPriceCents(), p.GetStock(), p.GetCategory())
}

func demo2Errors(ctx context.Context, c inventoryv1.InventoryServiceClient) {
	fmt.Println("\n=== 2. Status codes and error details ===")
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	_, err := c.GetProduct(ctx, &inventoryv1.GetProductRequest{Id: "nope"})

	// status.FromError is how you read a gRPC error. Never string-match.
	st, ok := status.FromError(err)
	fmt.Printf("  ok=%v code=%s msg=%q\n", ok, st.Code(), st.Message())

	// The structured details survive the network trip -- this is how a
	// caller can act on the failure programmatically.
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			fmt.Printf("  detail: reason=%s domain=%s meta=%v\n", info.GetReason(), info.GetDomain(), info.GetMetadata())
		}
	}

	// Comparing codes, the right way:
	if status.Code(err) == codes.NotFound {
		fmt.Println("  -> map this to HTTP 404 at the gateway")
	}

	_, err = c.GetProduct(ctx, &inventoryv1.GetProductRequest{Id: ""})
	fmt.Printf("  empty id -> %s\n", status.Code(err))
}

func demo3Deadline(ctx context.Context, c inventoryv1.InventoryServiceClient) {
	fmt.Println("\n=== 3. Deadline propagation (client 150ms, server needs 2s) ===")

	ctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.GetProduct(ctx, &inventoryv1.GetProductRequest{Id: "slow"})
	fmt.Printf("  failed after %v with %s\n", time.Since(start).Round(10*time.Millisecond), status.Code(err))
	fmt.Println("  note the server logged that IT saw the cancellation -- the")
	fmt.Println("  deadline travelled over the wire as the grpc-timeout header")
}

func demo4Cancel(ctx context.Context, c inventoryv1.InventoryServiceClient) {
	fmt.Println("\n=== 4. Explicit cancellation ===")

	ctx, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel() // user closed the tab / upstream gave up
	}()

	_, err := c.GetProduct(ctx, &inventoryv1.GetProductRequest{Id: "slow"})
	fmt.Printf("  code=%s (Canceled, not DeadlineExceeded -- different cause)\n", status.Code(err))
}

func demo5Panic(ctx context.Context, c inventoryv1.InventoryServiceClient) {
	fmt.Println("\n=== 5. A panicking handler ===")
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	_, err := c.GetProduct(ctx, &inventoryv1.GetProductRequest{Id: "boom"})
	fmt.Printf("  client sees: %s / %q  (no stack trace leaked)\n", status.Code(err), status.Convert(err).Message())
	fmt.Println("  and the process is still alive -- that is the recovery interceptor")
}

func demo6NoAuth(addr string) {
	fmt.Println("\n=== 6. A client with no credentials ===")

	// A second connection WITHOUT the auth interceptor.
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Println(err)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = inventoryv1.NewInventoryServiceClient(conn).GetProduct(ctx, &inventoryv1.GetProductRequest{Id: "p1"})
	fmt.Printf("  code=%s msg=%q\n", status.Code(err), status.Convert(err).Message())
}

func demo7ServerStream(ctx context.Context, c inventoryv1.InventoryServiceClient) {
	fmt.Println("\n=== 7. Server streaming ===")
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	stream, err := c.WatchStock(ctx, &inventoryv1.WatchStockRequest{
		ProductIds: []string{"p1", "p2"}, MaxEvents: 4,
	})
	if err != nil {
		log.Println(err)
		return
	}

	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break // normal end of stream -- NOT an error
		}
		if err != nil {
			fmt.Println("  stream error:", status.Code(err))
			return
		}
		e := msg.GetEvent()
		fmt.Printf("  event: %s stock=%d (delta %d)\n", e.GetProductId(), e.GetStock(), e.GetDelta())
	}
	fmt.Println("  stream closed cleanly")
}

func demo8ClientStream(ctx context.Context, c inventoryv1.InventoryServiceClient) {
	fmt.Println("\n=== 8. Client streaming ===")
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	stream, err := c.BulkUpsert(ctx)
	if err != nil {
		log.Println(err)
		return
	}

	batch := []*inventoryv1.Product{
		{Id: "p1", Name: "X-Shot Blaster (v2)", PriceCents: 3199, Stock: 50},
		{Id: "p3", Name: "Robo Dog", PriceCents: 4999, Stock: 12},
		{Id: "", Name: "broken row"}, // deliberately invalid
		{Id: "p4", Name: "Paint Roller", PriceCents: 1299, Stock: 80},
	}
	for _, p := range batch {
		if err := stream.Send(&inventoryv1.BulkUpsertRequest{Product: p}); err != nil {
			// A Send error means the server already ended the RPC; the real
			// reason arrives from CloseAndRecv, so break and read it there.
			break
		}
	}

	// CloseAndRecv: "I am done sending, give me the single response."
	sum, err := stream.CloseAndRecv()
	if err != nil {
		fmt.Println("  error:", status.Code(err))
		return
	}
	fmt.Printf("  created=%d updated=%d failed=%d errors=%v\n",
		sum.GetCreated(), sum.GetUpdated(), sum.GetFailed(), sum.GetErrors())
}

func demo9BidiStream(ctx context.Context, c inventoryv1.InventoryServiceClient) {
	fmt.Println("\n=== 9. Bidirectional streaming ===")
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	stream, err := c.Reserve(ctx)
	if err != nil {
		log.Println(err)
		return
	}

	// The two directions are INDEPENDENT, so reading runs in its own
	// goroutine. Trying to Send and Recv in one loop is the classic bidi
	// deadlock: you block on Recv while the server blocks waiting for Send.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				fmt.Println("  recv error:", status.Code(err))
				return
			}
			fmt.Printf("  <- %s ok=%-5v remaining=%-3d %s\n",
				resp.GetProductId(), resp.GetOk(), resp.GetRemaining(), resp.GetReason())
		}
	}()

	reqs := []*inventoryv1.ReserveRequest{
		{ProductId: "p2", Quantity: 3, IdempotencyKey: "k1"},
		{ProductId: "p2", Quantity: 3, IdempotencyKey: "k1"}, // retry -> replayed, not re-applied
		{ProductId: "p2", Quantity: 99, IdempotencyKey: "k2"},
		{ProductId: "ghost", Quantity: 1, IdempotencyKey: "k3"},
	}
	for _, r := range reqs {
		fmt.Printf("  -> reserve %d of %s (key %s)\n", r.Quantity, r.ProductId, r.IdempotencyKey)
		if err := stream.Send(r); err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond) // just so the output interleaves readably
	}

	// CloseSend ends OUR direction. The server sees io.EOF and returns,
	// which ends the other direction and unblocks our reader goroutine.
	_ = stream.CloseSend()
	<-done
	fmt.Println("  note request 2 was a retry: same key -> same answer, stock not double-decremented")
}

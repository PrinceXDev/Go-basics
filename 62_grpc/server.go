package main

// ---------------------------------------------------------------------------
// THE SERVER. One struct implementing the generated InventoryServiceServer
// interface -- all four streaming shapes, plus proper gRPC status codes.
// ---------------------------------------------------------------------------

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	inventoryv1 "example.com/go-basics/62_grpc/gen/inventory/v1"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type inventoryServer struct {
	// Embedding the Unimplemented struct is MANDATORY (mustEmbedUnimplemented...).
	// It is not boilerplate for its own sake: when someone adds an rpc to the
	// proto, your server keeps compiling and returns codes.Unimplemented for
	// the new method instead of failing the build. Forward compatibility by
	// default -- which is what you want when the proto and the server are
	// owned by different teams.
	inventoryv1.UnimplementedInventoryServiceServer

	mu       sync.RWMutex
	products map[string]*inventoryv1.Product
}

func newInventoryServer() *inventoryServer {
	s := &inventoryServer{products: map[string]*inventoryv1.Product{}}
	s.products["p1"] = &inventoryv1.Product{
		Id: "p1", Name: "X-Shot Blaster", PriceCents: 2999, Stock: 42,
		Category: inventoryv1.Category_CATEGORY_TOYS, UpdatedAt: timestamppb.Now(),
	}
	s.products["p2"] = &inventoryv1.Product{
		Id: "p2", Name: "Cordless Drill", PriceCents: 8999, Stock: 7,
		Category: inventoryv1.Category_CATEGORY_TOOLS, UpdatedAt: timestamppb.Now(),
	}
	return s
}

// ===========================================================================
// 1. UNARY
// ===========================================================================

func (s *inventoryServer) GetProduct(ctx context.Context, req *inventoryv1.GetProductRequest) (*inventoryv1.GetProductResponse, error) {
	// VALIDATE FIRST. proto3 has no "required" -- every field is optional on
	// the wire and arrives as its zero value. Validation is always your job.
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	// Always use the GETTERS (req.GetId(), not req.Id). They are nil-safe:
	// on a nil message the getter returns the zero value instead of panicking.
	if req.GetId() == "slow" {
		// Simulate a slow dependency so we can watch a client deadline fire.
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			// The client's deadline propagated to us automatically over the
			// wire (gRPC sends it as the grpc-timeout header). Stop working
			// the instant it expires -- this is how you avoid burning CPU on
			// answers nobody is waiting for any more.
			log.Printf("   [server] client went away: %v", ctx.Err())
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}

	if req.GetId() == "boom" {
		panic("simulated bug in a handler") // caught by the recovery interceptor
	}

	s.mu.RLock()
	p, ok := s.products[req.GetId()]
	s.mu.RUnlock()

	if !ok {
		// A GOOD gRPC error: correct code + machine-readable details.
		// The code drives the caller's retry logic, so getting it right
		// matters more than the message.
		st := status.New(codes.NotFound, "product not found")
		st, err := st.WithDetails(&errdetails.ErrorInfo{
			Reason:   "PRODUCT_NOT_FOUND",
			Domain:   "inventory.v1",
			Metadata: map[string]string{"product_id": req.GetId()},
		})
		if err != nil {
			return nil, status.Error(codes.NotFound, "product not found")
		}
		return nil, st.Err()
	}

	return &inventoryv1.GetProductResponse{Product: p}, nil
}

// ===========================================================================
// 2. SERVER STREAMING -- one request in, many messages out.
// ===========================================================================

func (s *inventoryServer) WatchStock(req *inventoryv1.WatchStockRequest, stream grpc.ServerStreamingServer[inventoryv1.WatchStockResponse]) error {
	ids := req.GetProductIds()
	if len(ids) == 0 {
		return status.Error(codes.InvalidArgument, "product_ids must not be empty")
	}
	max := req.GetMaxEvents()
	if max <= 0 || max > 100 {
		max = 5
	}

	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()

	for i := int32(0); i < max; i++ {
		select {
		// stream.Context() is cancelled when the client hangs up OR its
		// deadline expires. A streaming handler that does not select on it
		// is a goroutine leak waiting to happen.
		case <-stream.Context().Done():
			log.Printf("   [server] watch cancelled after %d events", i)
			return status.FromContextError(stream.Context().Err()).Err()

		case <-ticker.C:
			id := ids[int(i)%len(ids)]
			delta := int32(-1 - i%3)

			s.mu.Lock()
			p, ok := s.products[id]
			if ok {
				p.Stock += delta
				p.UpdatedAt = timestamppb.Now()
			}
			s.mu.Unlock()
			if !ok {
				continue
			}

			// Send blocks when the client is slow (HTTP/2 flow control) --
			// that is backpressure, and it is a feature. It stops a fast
			// producer from turning into unbounded server memory.
			if err := stream.Send(&inventoryv1.WatchStockResponse{
				Event: &inventoryv1.StockEvent{
					ProductId: id, Stock: p.Stock, Delta: delta, At: timestamppb.Now(),
				},
			}); err != nil {
				return err // client closed the stream
			}
		}
	}
	// Returning nil closes the stream cleanly with an OK status.
	return nil
}

// ===========================================================================
// 3. CLIENT STREAMING -- many messages in, one response out.
// ===========================================================================

func (s *inventoryServer) BulkUpsert(stream grpc.ClientStreamingServer[inventoryv1.BulkUpsertRequest, inventoryv1.BulkUpsertResponse]) error {
	summary := &inventoryv1.BulkUpsertResponse{}

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			// The client finished sending. io.EOF here is NORMAL -- it is the
			// "end of stream" signal, not a failure. SendAndClose writes the
			// single response and ends the RPC.
			return stream.SendAndClose(summary)
		}
		if err != nil {
			return err // a real transport error, or the client cancelled
		}

		p := req.GetProduct()
		if p.GetId() == "" || p.GetName() == "" {
			summary.Failed++
			summary.Errors = append(summary.Errors, "missing id or name")
			continue
		}

		s.mu.Lock()
		if _, exists := s.products[p.GetId()]; exists {
			summary.Updated++
		} else {
			summary.Created++
		}
		p.UpdatedAt = timestamppb.Now()
		s.products[p.GetId()] = p
		s.mu.Unlock()
	}
}

// ===========================================================================
// 4. BIDIRECTIONAL STREAMING -- both directions, independently, one connection.
// ===========================================================================

func (s *inventoryServer) Reserve(stream grpc.BidiStreamingServer[inventoryv1.ReserveRequest, inventoryv1.ReserveResponse]) error {
	// Idempotency keys: the caller may retry a reservation after a network
	// blip, and we must not double-decrement stock. This is per-stream here;
	// in production it is a Redis/Postgres key with a TTL.
	seen := map[string]*inventoryv1.ReserveResponse{}

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil // client closed its side; we close ours by returning
		}
		if err != nil {
			return err
		}

		if prev, ok := seen[req.GetIdempotencyKey()]; ok && req.GetIdempotencyKey() != "" {
			// Replay the previous answer instead of applying the change twice.
			if err := stream.Send(prev); err != nil {
				return err
			}
			continue
		}

		resp := s.reserve(req)
		if k := req.GetIdempotencyKey(); k != "" {
			seen[k] = resp
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

func (s *inventoryServer) reserve(req *inventoryv1.ReserveRequest) *inventoryv1.ReserveResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.products[req.GetProductId()]
	if !ok {
		return &inventoryv1.ReserveResponse{ProductId: req.GetProductId(), Ok: false, Reason: "unknown product"}
	}
	if req.GetQuantity() <= 0 {
		return &inventoryv1.ReserveResponse{ProductId: p.Id, Ok: false, Remaining: p.Stock, Reason: "quantity must be positive"}
	}
	if p.Stock < req.GetQuantity() {
		return &inventoryv1.ReserveResponse{
			ProductId: p.Id, Ok: false, Remaining: p.Stock,
			Reason: fmt.Sprintf("only %d left", p.Stock),
		}
	}
	p.Stock -= req.GetQuantity()
	p.UpdatedAt = timestamppb.Now()
	return &inventoryv1.ReserveResponse{ProductId: p.Id, Ok: true, Remaining: p.Stock}
}

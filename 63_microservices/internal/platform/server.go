package platform

// ===========================================================================
// Server + client construction, health checking and graceful shutdown --
// identical in every service, so it lives here exactly once.
// ===========================================================================

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
)

type Server struct {
	GRPC   *grpc.Server
	Health *health.Server
	addr   string
	log    *slog.Logger
}

// NewServer builds a gRPC server with the house standards already applied.
func NewServer(addr string, log *slog.Logger) *Server {
	s := grpc.NewServer(
		UnaryServerChain(log),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
			// Recycle long-lived connections so a new replica actually gets
			// traffic. Without MaxConnectionAge, a client that connected
			// before you scaled up keeps talking to the same old pod forever.
			MaxConnectionAge:      30 * time.Minute,
			MaxConnectionAgeGrace: 30 * time.Second,
		}),
	)

	// THE HEALTH SERVICE is not optional. Kubernetes, the load balancer and
	// gRPC's own health-checking client all speak grpc.health.v1.
	//
	//	SERVING     -> send me traffic
	//	NOT_SERVING -> drain me (this is what you set FIRST on shutdown,
	//	               before you stop accepting, so the LB removes you
	//	               while you finish in-flight work)
	h := health.NewServer()
	healthpb.RegisterHealthServer(s, h)

	// Reflection lets grpcurl/postman discover your API with no .proto file.
	// Wonderful in dev. Turn it OFF in production: it publishes your entire
	// schema to anyone who can reach the port.
	reflection.Register(s)

	return &Server{GRPC: s, Health: h, addr: addr, log: log}
}

// Run serves until ctx is cancelled, then shuts down gracefully.
//
// THE DRAINING ORDER matters and it is the same as lesson 42:
//
//  1. flip health to NOT_SERVING   -> the LB stops sending NEW requests
//  2. wait a beat                  -> the LB needs a moment to notice
//  3. GracefulStop                 -> finish IN-FLIGHT requests, refuse new
//  4. hard Stop after a timeout    -> a stuck stream must not block forever
//
// Skip step 1 and you drop requests the load balancer routed a millisecond
// before you died. That is the difference between a clean deploy and a
// deploy that shows up as a blip on your error dashboard.
func (s *Server) Run(ctx context.Context, serviceName string) error {
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
	}

	s.Health.SetServingStatus(serviceName, healthpb.HealthCheckResponse_SERVING)
	s.Health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING) // overall

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("serving", "addr", lis.Addr().String())
		errCh <- s.GRPC.Serve(lis)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	s.log.Info("shutdown started: draining")
	s.Health.SetServingStatus(serviceName, healthpb.HealthCheckResponse_NOT_SERVING)
	s.Health.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	time.Sleep(150 * time.Millisecond) // in k8s this is more like 5-10s

	done := make(chan struct{})
	go func() {
		s.GRPC.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		s.log.Info("shutdown complete")
	case <-time.After(10 * time.Second):
		s.log.Warn("graceful shutdown timed out, forcing")
		s.GRPC.Stop()
	}
	return nil
}

// SignalContext cancels on Ctrl+C / SIGTERM. SIGTERM is what Kubernetes
// sends before it kills the pod, so this is the hook that makes rolling
// deploys invisible to users.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// CLIENT
// ---------------------------------------------------------------------------

// Dial creates a long-lived connection to another service.
//
// ONE ClientConn per target, for the lifetime of the process. It is
// goroutine-safe, multiplexes every concurrent call over one HTTP/2
// connection, reconnects on its own, and load-balances across the addresses
// its resolver returns. Creating one per request is the single most common
// gRPC performance bug.
func Dial(target string) (*grpc.ClientConn, error) {
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()), // mTLS in prod
		grpc.WithChainUnaryInterceptor(ForwardRequestIDUnary()),

		// SERVICE DISCOVERY, in one line. The target string picks a resolver:
		//
		//	"127.0.0.1:50051"          -> a single host (what we use here)
		//	"dns:///orders.svc:50051"  -> DNS; re-resolves, gets all A records
		//	"kubernetes:///orders"     -> a headless Service's pod IPs
		//	"consul://..." / "etcd://" -> a registry, via a custom resolver
		//
		// Combined with round_robin below, "dns:///" + a k8s headless
		// Service IS client-side load balancing. No sidecar required.
		grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`),

		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                20 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	)
}

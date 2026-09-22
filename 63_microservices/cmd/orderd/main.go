// Command orderd runs the order service. Unlike userd it has a dependency,
// so it also dials the user service at startup.
//
// NOTE what it does NOT do: block waiting for the user service to be up.
// grpc.NewClient is lazy and reconnects on its own, so a service must be
// able to start while its dependencies are still booting. A service that
// crash-loops because a dependency is not ready yet turns one slow pod
// into a cluster-wide restart storm.
package main

import (
	"log"
	"os"

	orderv1 "example.com/go-basics/63_microservices/gen/order/v1"
	userv1 "example.com/go-basics/63_microservices/gen/user/v1"
	"example.com/go-basics/63_microservices/internal/orderservice"
	"example.com/go-basics/63_microservices/internal/platform"
)

func main() {
	addr := env("ORDER_GRPC_ADDR", "127.0.0.1:50052")
	userAddr := env("USER_GRPC_ADDR", "127.0.0.1:50051")

	logger := platform.NewLogger("orderd", os.Getenv("LOG_FORMAT") == "json")

	userConn, err := platform.Dial(userAddr)
	if err != nil {
		log.Fatalf("dial user service: %v", err)
	}
	defer userConn.Close()

	svc := orderservice.New(
		userv1.NewUserServiceClient(userConn),
		func(name string, from, to platform.BreakerState) {
			// An alert-worthy event. In production: a Prometheus gauge plus
			// a PagerDuty rule on "open for more than a minute".
			logger.Warn("circuit breaker state change", "dependency", name, "from", from.String(), "to", to.String())
		},
	)

	srv := platform.NewServer(addr, logger)
	orderv1.RegisterOrderServiceServer(srv.GRPC, svc)

	ctx, stop := platform.SignalContext()
	defer stop()

	if err := srv.Run(ctx, "order.v1.OrderService"); err != nil {
		log.Fatal(err)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

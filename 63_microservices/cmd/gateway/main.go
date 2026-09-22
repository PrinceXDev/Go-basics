// Command gateway runs the public HTTP edge.
package main

import (
	"log"
	"os"

	orderv1 "example.com/go-basics/63_microservices/gen/order/v1"
	userv1 "example.com/go-basics/63_microservices/gen/user/v1"
	"example.com/go-basics/63_microservices/internal/gateway"
	"example.com/go-basics/63_microservices/internal/platform"
)

func main() {
	httpAddr := env("GATEWAY_HTTP_ADDR", "127.0.0.1:8080")
	userAddr := env("USER_GRPC_ADDR", "127.0.0.1:50051")
	orderAddr := env("ORDER_GRPC_ADDR", "127.0.0.1:50052")

	logger := platform.NewLogger("gateway", os.Getenv("LOG_FORMAT") == "json")

	// One ClientConn per downstream service, created ONCE at startup and
	// shared by every request. Never per-request.
	userConn, err := platform.Dial(userAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer userConn.Close()

	orderConn, err := platform.Dial(orderAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer orderConn.Close()

	g := gateway.New(
		userv1.NewUserServiceClient(userConn),
		orderv1.NewOrderServiceClient(orderConn),
		logger,
	)

	ctx, stop := platform.SignalContext()
	defer stop()

	if err := g.Serve(ctx, httpAddr); err != nil {
		log.Fatal(err)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

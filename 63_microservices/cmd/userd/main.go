// Command userd runs the user service as its own process.
//
// A main() in a well-factored Go service does exactly four things:
//
//  1. read config (env only -- 12-factor; no config files baked into images)
//  2. construct dependencies
//  3. wire them together
//  4. run until a signal arrives, then shut down gracefully
//
// If main() contains business logic, it cannot be tested. Keep it dumb.
package main

import (
	"log"
	"os"

	userv1 "example.com/go-basics/63_microservices/gen/user/v1"
	"example.com/go-basics/63_microservices/internal/platform"
	"example.com/go-basics/63_microservices/internal/userservice"
)

func main() {
	addr := env("USER_GRPC_ADDR", "127.0.0.1:50051")

	logger := platform.NewLogger("userd", os.Getenv("LOG_FORMAT") == "json")
	srv := platform.NewServer(addr, logger)
	userv1.RegisterUserServiceServer(srv.GRPC, userservice.New())

	ctx, stop := platform.SignalContext()
	defer stop()

	if err := srv.Run(ctx, "user.v1.UserService"); err != nil {
		log.Fatal(err)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

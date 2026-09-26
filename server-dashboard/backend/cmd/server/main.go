// Command server is the interview-question demo: N servers report health
// and stats, and a dashboard shows them live.
//
// Architecture:
//
//	10 servers --POST /api/servers/{id}/health--> registry.Registry (RWMutex map)
//	                                                     |
//	                                                     v
//	                                              hub.Hub.Broadcast
//	                                                     |
//	                                     fan-out over WebSocket, one
//	                                     goroutine per connected browser tab
//	                                                     |
//	                                                     v
//	                                          React dashboard (useState)
//
// This binary also runs a `simulator` that plays the role of the 10
// servers, so `go run ./cmd/server` is a complete, runnable demo with no
// other services required.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"serverdashboard/internal/api"
	"serverdashboard/internal/hub"
	"serverdashboard/internal/registry"
	"serverdashboard/internal/simulator"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg := registry.New()
	h := hub.New()
	go h.Run(ctx)

	a := api.New(reg, h)
	mux := http.NewServeMux()
	a.Routes(mux)

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           api.CORS(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// The simulator plays the role of the 10 real servers: it calls
	// a.IngestStats, the exact same path a real POST /api/servers/{id}/health
	// handler goes through.
	ids := serverIDs(10)
	simDone := make(chan struct{})
	go func() {
		simulator.Run(ctx, ids, a.IngestStats)
		close(simDone)
	}()

	go func() {
		slog.Info("server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
	<-simDone
}

func serverIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("srv-%02d", i+1)
	}
	return ids
}

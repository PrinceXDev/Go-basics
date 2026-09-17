package router

import (
	"log"
	"net/http"
	"time"
)

// registerable is any handler that can attach its routes to a mux — user,
// product and order handlers all implement this.
type registerable interface {
	Register(mux *http.ServeMux)
}

// New builds the top-level mux, wires in every entity's routes, plus a
// health check, and wraps it all with basic request logging.
func New(handlers ...registerable) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	for _, h := range handlers {
		h.Register(mux)
	}

	return logging(mux)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %v", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

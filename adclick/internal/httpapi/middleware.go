package httpapi

import (
	"encoding/json"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

// Flush keeps SSE working through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// middleware: panic recovery, latency metrics (labelled by route pattern, not
// raw URL, to keep label cardinality bounded) and one access log per request.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		defer func() {
			if rec := recover(); rec != nil {
				s.Log.Error("panic in handler", "panic", rec, "stack", string(debug.Stack()))
				if sw.code == http.StatusOK {
					writeErr(sw, http.StatusInternalServerError, "internal error")
				}
			}
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			d := time.Since(start)
			if !strings.HasSuffix(route, "/stream") && route != "GET /metrics" {
				s.Metrics.HTTPDuration.WithLabelValues(route, r.Method, itoa(sw.code)).Observe(d.Seconds())
			}
			if route != "GET /metrics" && route != "GET /healthz" {
				s.Log.Info("request", "route", route, "status", sw.code, "dur_ms", d.Milliseconds())
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

func deviceFromUA(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "ipad"), strings.Contains(l, "tablet"):
		return "tablet"
	case strings.Contains(l, "mobile"), strings.Contains(l, "android"), strings.Contains(l, "iphone"):
		return "mobile"
	case l == "":
		return "other"
	}
	return "desktop"
}

func browserFromUA(ua string) string {
	switch {
	case strings.Contains(ua, "Edg/"):
		return "edge"
	case strings.Contains(ua, "Firefox/"):
		return "firefox"
	case strings.Contains(ua, "Chrome/"):
		return "chrome"
	case strings.Contains(ua, "Safari/"):
		return "safari"
	}
	return "other"
}

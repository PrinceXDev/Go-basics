package main

// ===========================================================================
// THE API GATEWAY — the single front door to every backend service.
//
//   mobile app / netbanking / partner
//                  |
//                  v
//   +-------------------------------------------------------------+
//   | GATEWAY  (each line below is one middleware, in this order) |
//   |  1. request ID       give the journey one traceable ID      |
//   |  2. access log       who, what, status, how long            |
//   |  3. recover          a panic becomes a 500, not a crash     |
//   |  4. body limit       reject huge payloads before reading    |
//   |  5. authenticate     verify the customer's JWT, ONCE        |
//   |  6. rate limit       per customer, after we know who it is  |
//   |  7. timeout          the whole request gets a deadline      |
//   |  8. route + proxy    strip /api/v1, pick upstream, forward  |
//   +-------------------------------------------------------------+
//          |                         |
//          v                         v
//     accounts svc              payments svc
//
// WHAT A GATEWAY SHOULD NOT DO: business logic. "Is the balance enough?"
// belongs in accounts; "is this account alice's?" belongs in accounts too.
// A gateway full of business rules becomes a bottleneck every team must
// change — the "smart gateway, dumb services" anti-pattern.
// ===========================================================================

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

type Route struct {
	Pattern  string // Go 1.22 mux pattern, e.g. "GET /api/v1/accounts/{id}"
	Service  string // logical name, used as the service-token audience
	Upstream string // base URL; in Kubernetes this would be http://accounts.svc:8080
}

type GatewayConfig struct {
	UserKey      []byte
	Services     ServiceAuth
	Routes       []Route
	RatePerSec   float64
	Burst        int
	Timeout      time.Duration
	MaxBodyBytes int64
	Log          *slog.Logger
}

func NewGateway(cfg GatewayConfig) (http.Handler, error) {
	// One shared Transport = one shared connection pool. Creating a new
	// Transport per request would open a fresh TCP (and TLS) connection
	// every time and exhaust ports under load.
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   50, // default is only 2 — far too low for a gateway
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	for _, rt := range cfg.Routes {
		p, err := newProxy(rt, cfg.Services, transport, cfg.Log)
		if err != nil {
			return nil, err
		}
		mux.Handle(rt.Pattern, p)
	}

	limiter := newKeyedLimiter(cfg.RatePerSec, cfg.Burst)
	public := map[string]bool{"/healthz": true}

	return chain(mux,
		gwRequestID,
		gwAccessLog(cfg.Log),
		gwRecover(cfg.Log),
		gwBodyLimit(cfg.MaxBodyBytes),
		gwAuthenticate(cfg.UserKey, public),
		gwRateLimit(limiter, public),
		gwTimeout(cfg.Timeout),
	), nil
}

// newProxy builds a reverse proxy for one route. httputil.ReverseProxy is
// what Kong/Envoy/NGINX do at their core: copy the request to an upstream,
// stream the response back.
func newProxy(rt Route, svc ServiceAuth, tr http.RoundTripper, log *slog.Logger) (*httputil.ReverseProxy, error) {
	target, err := url.Parse(rt.Upstream)
	if err != nil {
		return nil, fmt.Errorf("route %q: %w", rt.Pattern, err)
	}
	return &httputil.ReverseProxy{
		Transport: tr,
		// Rewrite (not the older Director) starts from a clean outbound
		// request and strips hop-by-hop headers for us.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// Public URL is versioned (/api/v1/...); services don't care.
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, "/api/v1")
			pr.Out.URL.RawPath = ""
			pr.SetXForwarded()

			ctx := pr.In.Context()
			// SECURITY: never forward identity headers the CLIENT sent. If we
			// did, anyone could send "X-User-ID: bob" and become bob.
			pr.Out.Header.Del(HeaderUserID)
			pr.Out.Header.Set(HeaderUserID, userIDFrom(ctx))
			pr.Out.Header.Set(HeaderRequestID, requestIDFrom(ctx))
			// Swap the customer's token for a service token: internal services
			// never see (and so can never leak) the customer's credential.
			pr.Out.Header.Set("Authorization", "Bearer "+svc.Mint("gateway", rt.Service))
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			status, code := http.StatusBadGateway, "upstream_unavailable"
			if errors.Is(err, context.DeadlineExceeded) {
				status, code = http.StatusGatewayTimeout, "upstream_timeout"
			}
			log.Warn("proxy error", "rid", requestIDFrom(r.Context()), "service", rt.Service, "err", err)
			// Never echo raw internal errors (hostnames, ports) to customers.
			writeError(w, r, status, code, rt.Service+" did not respond in time")
		},
	}, nil
}

func gwRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Accept a client ID only if it looks sane; otherwise mint our own.
		rid := r.Header.Get(HeaderRequestID)
		if rid == "" || len(rid) > 64 {
			rid = newID("req-")
		}
		w.Header().Set(HeaderRequestID, rid) // customer support can ask for this
		next.ServeHTTP(w, r.WithContext(withValue(r.Context(), ctxRequestID, rid)))
	})
}

func gwAccessLog(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			// Never log Authorization headers or full request bodies: they
			// contain tokens, account numbers and PII.
			log.Info("gateway", "rid", requestIDFrom(r.Context()), "method", r.Method,
				"path", r.URL.Path, "status", rec.status, "took", time.Since(start).Round(time.Millisecond))
		})
	}
}

func gwRecover(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler { // the proxy uses this on purpose
						panic(v)
					}
					log.Error("panic", "rid", requestIDFrom(r.Context()), "value", v)
					writeError(w, r, http.StatusInternalServerError, "internal", "something went wrong")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func gwBodyLimit(max int64) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > max {
				writeError(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
				return
			}
			// Covers chunked bodies whose length is unknown up front.
			r.Body = http.MaxBytesReader(w, r.Body, max)
			next.ServeHTTP(w, r)
		})
	}
}

func gwAuthenticate(key []byte, public map[string]bool) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if public[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			raw, ok := bearer(r)
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer realm="bank"`)
				writeError(w, r, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
				return
			}
			claims, err := verifyUserToken(key, raw)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				writeError(w, r, http.StatusUnauthorized, "invalid_token", "token rejected")
				return
			}
			next.ServeHTTP(w, r.WithContext(withValue(r.Context(), ctxUserID, claims.Subject)))
		})
	}
}

// Rate limit AFTER auth so the key is the customer, not the IP: thousands
// of customers can share one office/mobile-carrier IP (NAT).
func gwRateLimit(l *keyedLimiter, public map[string]bool) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if public[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			if !l.Allow(userIDFrom(r.Context())) {
				w.Header().Set("Retry-After", "1")
				writeError(w, r, http.StatusTooManyRequests, "rate_limited", "slow down")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// gwTimeout gives the whole request one deadline. The proxy passes this
// context to the upstream call, so when time is up the upstream request is
// cancelled too — no orphaned work piling up behind a slow service.
func gwTimeout(d time.Duration) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

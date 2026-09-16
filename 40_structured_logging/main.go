package main

// ============================================================================
// CONCEPT: `log/slog` — structured, levelled logging (stdlib since Go 1.21).
//
// WHY THIS MATTERS
// `log.Printf("user %s failed: %v", name, err)` produces a string. A human
// can read it; a log aggregator cannot. You can't filter it, you can't
// alert on it, and you certainly can't ask "how many 500s did user ada get
// last Tuesday". Structured logging emits KEY/VALUE pairs — usually JSON —
// so your logs become queryable data.
//
// slog is now in the standard library, so there is no longer a reason to
// reach for logrus or zap in a new project unless you need extreme
// performance (zap) or you're maintaining existing code.
//
// JS/TS comparison: pino. Same idea — JSON lines, levels, child loggers
// with bound fields.
// ============================================================================

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// A request-scoped logger is carried on the context, exactly like the
// request ID in lesson 39.
type ctxKey string

const loggerKey ctxKey = "logger"

func main() {
	// ---------- 1. handlers: text vs JSON ----------
	// A slog.Logger writes through a HANDLER. Two ship with the stdlib:
	//   TextHandler  key=value, readable — use in local development
	//   JSONHandler  one JSON object per line — use in production
	fmt.Println("== TextHandler (development) ==")
	textLog := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	textLog.Info("server starting", "port", 8080, "env", "dev")

	fmt.Println()
	fmt.Println("== JSONHandler (production) ==")
	jsonLog := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo, // Debug messages are dropped
	}))
	jsonLog.Debug("this never appears — below the level")
	jsonLog.Info("server starting", "port", 8080, "env", "prod")

	// ---------- 2. levels ----------
	// Debug(-4) < Info(0) < Warn(4) < Error(8). Anything below the
	// handler's Level is discarded cheaply — the arguments aren't even
	// formatted.
	fmt.Println()
	fmt.Println("== levels ==")
	jsonLog.Warn("disk nearly full", "free_pct", 7)
	jsonLog.Error("payment failed", "order_id", "ord_991", "err", errors.New("card declined"))

	// A LevelVar lets you change the level at RUNTIME — wire it to a
	// SIGHUP handler or an admin endpoint to turn on debug logging in
	// production without a redeploy.
	levelVar := new(slog.LevelVar)
	levelVar.Set(slog.LevelInfo)
	dynamic := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar}))
	dynamic.Debug("hidden")
	levelVar.Set(slog.LevelDebug) // flip the switch
	dynamic.Debug("now visible, no restart needed")

	// ---------- 3. attributes: typed vs loose ----------
	// The variadic "key", value form is convenient but unchecked — an odd
	// number of arguments produces a "!BADKEY" entry rather than a compile
	// error. slog.String/Int/Duration/Any are typed and slightly faster.
	fmt.Println()
	fmt.Println("== attributes ==")
	jsonLog.Info("loose form", "user", "ada", "attempts", 3)
	jsonLog.LogAttrs(context.Background(), slog.LevelInfo, "typed form",
		slog.String("user", "ada"),
		slog.Int("attempts", 3),
		slog.Duration("took", 120*time.Millisecond),
		slog.Bool("cached", false),
	)
	// An odd number of arguments produces a "!BADKEY" entry instead of a
	// compile error. `go vet` DOES catch the literal form
	// (`log.Info("msg", "orphan")`), which is a good reason to run it in
	// CI — but it can't see through a slice, as here.
	orphaned := []any{"orphan"}
	jsonLog.Info("odd number of args", orphaned...) // -> !BADKEY

	// Grouping nests related fields into a sub-object.
	jsonLog.Info("request done",
		slog.Group("http",
			slog.String("method", "GET"),
			slog.String("path", "/users/1"),
			slog.Int("status", 200),
		),
		slog.Group("db", slog.Int("queries", 3), slog.Duration("time", 4*time.Millisecond)),
	)

	// ---------- 4. With: child loggers ----------
	// .With returns a NEW logger that carries those attributes on every
	// subsequent call. This is the single most useful slog feature: bind
	// the request id once, and every log line in that request has it.
	fmt.Println()
	fmt.Println("== With: bound context ==")
	base := jsonLog.With("service", "api", "version", "1.4.2")
	reqLog := base.With("request_id", "req-0042", "user_id", 7)
	reqLog.Info("handling request", "path", "/checkout")
	reqLog.Warn("slow query", "ms", 310)
	reqLog.Info("request complete", "status", 200)

	// WithGroup nests everything that follows.
	jsonLog.WithGroup("worker").Info("job done", "id", 12, "retries", 0)

	// ---------- 5. the default logger ----------
	// slog.Info/Warn/Error (package-level) use a global default. Set it
	// once at startup so libraries and the old `log` package feed into the
	// same structured stream.
	fmt.Println()
	fmt.Println("== default logger ==")
	slog.SetDefault(jsonLog)
	slog.Info("this went through the package-level API")
	// Bonus: after SetDefault, the legacy `log` package also routes here,
	// so third-party libraries calling log.Printf get structured output.

	// ---------- 6. context-scoped loggers ----------
	// Put the request logger on the context so every layer — handler,
	// service, repository — logs with the same bound fields, without you
	// threading a *slog.Logger through every signature.
	fmt.Println()
	fmt.Println("== logger on the context ==")
	ctx := WithLogger(context.Background(), base.With("request_id", "req-0099"))
	doWork(ctx)

	// ---------- 7. ReplaceAttr: redaction and renaming ----------
	// The hook every production service needs: strip secrets, rename keys
	// to match your log platform's schema, shorten source paths.
	fmt.Println()
	fmt.Println("== ReplaceAttr: redaction ==")
	safe := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     slog.LevelInfo,
		AddSource: true, // includes file:line — costs a little performance
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case "password", "token", "api_key", "authorization":
				return slog.String(a.Key, "[REDACTED]")
			case slog.TimeKey:
				// Fixed timestamp here so this lesson's output is stable;
				// in production you'd normally leave time alone, or format
				// it as RFC3339 for your log platform.
				return slog.String("ts", "2026-01-01T00:00:00Z")
			case slog.MessageKey:
				return slog.Attr{Key: "msg", Value: a.Value} // rename if needed
			}
			return a
		},
	}))
	safe.Info("login attempt",
		"user", "ada",
		"password", "hunter2", // never actually log this — but now it's safe
		"token", "eyJhbGciOi...",
	)

	// ---------- 8. errors ----------
	// There's no slog.Error attribute constructor, because an error isn't a
	// primitive. The convention is a key called "err" (or "error") holding
	// err.Error(). Wrapped errors (lesson 35) keep their full chain.
	fmt.Println()
	fmt.Println("== logging errors ==")
	err := fmt.Errorf("charge customer: %w", errors.New("insufficient funds"))
	safe.Error("checkout failed", "err", err, "order_id", "ord_991")
}

func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// LoggerFrom always returns a usable logger — never nil. A logging helper
// that can panic is worse than no logging.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

func doWork(ctx context.Context) {
	log := LoggerFrom(ctx) // deep inside the call stack, still has request_id
	log.Info("started work")
	log.Info("finished work", "items", 42)
}

// ----------------------------------------------------------------------------
// WIRING IT INTO LESSON 39's MIDDLEWARE
//
//	func Logging(base *slog.Logger) Middleware {
//	    return func(next http.Handler) http.Handler {
//	        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//	            start := time.Now()
//	            l := base.With("request_id", RequestIDFrom(r.Context()))
//	            rec := &statusRecorder{ResponseWriter: w}
//	            next.ServeHTTP(rec, r.WithContext(WithLogger(r.Context(), l)))
//	            l.Info("request",
//	                "method", r.Method, "path", r.URL.Path,
//	                "status", rec.status, "bytes", rec.bytes,
//	                "duration_ms", time.Since(start).Milliseconds())
//	        })
//	    }
//	}
//
// RULES
//   1. JSONHandler in production, TextHandler locally. Decide from config
//      (lesson 41), not from a code change.
//   2. Log to STDOUT, not to a file. The platform (systemd, Docker, k8s)
//      collects it. Never build log rotation into your app.
//   3. One log line per request at Info, with status + duration. Anything
//      more is Debug.
//   4. Bind context with .With once, don't repeat fields at every call.
//   5. Use consistent key names across the whole service — "user_id"
//      everywhere, never "userId" here and "uid" there. You cannot query
//      what you can't predict.
//   6. Never log secrets, tokens, passwords, full card numbers, or PII.
//      Enforce it with ReplaceAttr so it can't happen by accident.
//   7. Error level means "a human should look at this". A 404 is not an
//      error. If everything is an error, nothing is.
//   8. slog is ~2x slower than zap for extreme throughput. For the 99% of
//      services that log a few thousand lines a second, that's irrelevant.
// ----------------------------------------------------------------------------

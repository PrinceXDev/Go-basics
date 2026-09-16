package main

// ============================================================================
// CONCEPT: configuration — environment variables, flags, defaults, and
// validating it all at startup.
//
// WHY THIS MATTERS
// Hard-coded ports, connection strings and API keys are the reason "it
// works on my machine". The 12-factor rule is: config lives in the
// ENVIRONMENT, because that's the one thing that differs between dev,
// staging and production while the binary stays byte-identical.
//
// The pattern below is the one most Go services use:
//   1. one Config struct — the single source of truth
//   2. load from defaults <- env vars <- flags (each layer overrides)
//   3. VALIDATE and fail loudly at startup, never mid-request
//   4. pass the struct down explicitly; no global config package
//
// JS/TS comparison: `process.env` + dotenv + zod. Go has no dotenv in the
// stdlib (add github.com/joho/godotenv for local dev), and no automatic
// coercion — you parse every value yourself, which is why the helpers below
// exist.
// ============================================================================

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// THE CONFIG STRUCT
// Group related settings with nested structs. Note that Secret fields use a
// dedicated type so they can't be printed by accident (see below).
// ---------------------------------------------------------------------------

type Config struct {
	Env      string // dev | staging | prod
	LogLevel string // debug | info | warn | error

	Server struct {
		Host            string
		Port            int
		ReadTimeout     time.Duration
		WriteTimeout    time.Duration
		ShutdownTimeout time.Duration
	}

	DB struct {
		DSN          Secret
		MaxOpenConns int
	}

	Features struct {
		EnableSignup bool
		AllowedHosts []string
	}
}

// Secret is a string that refuses to print itself. Because it implements
// String() (lesson 12/14), fmt verbs, %v inside a struct dump, and slog all
// get the masked version — so a stray log line can't leak your DSN.
type Secret string

func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "[REDACTED]"
}

// Reveal is the deliberately awkward accessor. Making the safe thing easy
// and the unsafe thing explicit is the whole point.
func (s Secret) Reveal() string { return string(s) }

// ---------------------------------------------------------------------------
// LOADING
// ---------------------------------------------------------------------------

func Load(args []string) (Config, error) {
	var c Config

	// LAYER 1: defaults, written as plain Go. Sensible defaults mean a
	// developer can clone the repo and run it with zero setup.
	c.Env = "dev"
	c.LogLevel = "info"
	c.Server.Host = "127.0.0.1"
	c.Server.Port = 8080
	c.Server.ReadTimeout = 10 * time.Second
	c.Server.WriteTimeout = 15 * time.Second
	c.Server.ShutdownTimeout = 20 * time.Second
	c.DB.MaxOpenConns = 25
	c.Features.EnableSignup = true
	c.Features.AllowedHosts = []string{"localhost"}

	// LAYER 2: environment variables override defaults.
	// Prefix everything with your service name so you don't collide with
	// PATH, HOME, or another service's vars.
	c.Env = envStr("APP_ENV", c.Env)
	c.LogLevel = envStr("APP_LOG_LEVEL", c.LogLevel)
	c.Server.Host = envStr("APP_HOST", c.Server.Host)
	c.DB.DSN = Secret(envStr("APP_DB_DSN", c.DB.DSN.Reveal()))
	c.Features.AllowedHosts = envCSV("APP_ALLOWED_HOSTS", c.Features.AllowedHosts)

	// Typed env vars can FAIL to parse. Collect the failures rather than
	// stopping at the first one — errors.Join from lesson 35 is exactly
	// right here, because you want one message listing everything wrong.
	var errs []error
	var err error

	if c.Server.Port, err = envInt("APP_PORT", c.Server.Port); err != nil {
		errs = append(errs, err)
	}
	if c.DB.MaxOpenConns, err = envInt("APP_DB_MAX_CONNS", c.DB.MaxOpenConns); err != nil {
		errs = append(errs, err)
	}
	if c.Server.ReadTimeout, err = envDur("APP_READ_TIMEOUT", c.Server.ReadTimeout); err != nil {
		errs = append(errs, err)
	}
	if c.Features.EnableSignup, err = envBool("APP_ENABLE_SIGNUP", c.Features.EnableSignup); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}

	// LAYER 3: command-line flags beat everything. Flags are for operator
	// overrides ("just this once, run on 9090") and for things a human
	// types; env vars are for deployment config.
	//
	// Using an explicit FlagSet rather than the global `flag` package means
	// this function is testable — you can call Load() with fake args.
	fs := flag.NewFlagSet("app", flag.ContinueOnError)
	fs.StringVar(&c.Env, "env", c.Env, "environment: dev|staging|prod")
	fs.IntVar(&c.Server.Port, "port", c.Server.Port, "HTTP port")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "debug|info|warn|error")
	// Note each default is the CURRENT value, so a flag that isn't passed
	// leaves the env/default value untouched. This is the trick that makes
	// three-layer precedence work with the stdlib flag package.
	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}

	// LAYER 4: validate. This is the step people skip, and then discover at
	// 3am that APP_PORT was "8O80" with a letter O.
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks every invariant and reports ALL problems at once.
func (c Config) Validate() error {
	var errs []error

	if !oneOf(c.Env, "dev", "staging", "prod") {
		errs = append(errs, fmt.Errorf("APP_ENV: %q must be dev|staging|prod", c.Env))
	}
	if !oneOf(c.LogLevel, "debug", "info", "warn", "error") {
		errs = append(errs, fmt.Errorf("APP_LOG_LEVEL: %q is not a valid level", c.LogLevel))
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		errs = append(errs, fmt.Errorf("APP_PORT: %d is out of range", c.Server.Port))
	}
	if c.DB.MaxOpenConns < 1 {
		errs = append(errs, fmt.Errorf("APP_DB_MAX_CONNS: must be >= 1, got %d", c.DB.MaxOpenConns))
	}
	// Conditional requirements: a DSN is optional in dev, mandatory in prod.
	if c.Env == "prod" {
		if c.DB.DSN == "" {
			errs = append(errs, errors.New("APP_DB_DSN: required when APP_ENV=prod"))
		}
		if c.Server.Host == "127.0.0.1" {
			errs = append(errs, errors.New("APP_HOST: must not be loopback in prod"))
		}
	}
	return errors.Join(errs...)
}

func (c Config) Addr() string { return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port) }

// ---------------------------------------------------------------------------
// TYPED ENV HELPERS
// Go's os.Getenv only returns strings, and returns "" for both "unset" and
// "set to empty". os.LookupEnv distinguishes them — use it.
// ---------------------------------------------------------------------------

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		// Include the key AND the bad value — the operator needs both.
		return def, fmt.Errorf("%s: %q is not an integer", key, v)
	}
	return n, nil
}

func envBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v) // accepts 1/0, t/f, true/false, TRUE/FALSE
	if err != nil {
		return def, fmt.Errorf("%s: %q is not a boolean", key, v)
	}
	return b, nil
}

func envDur(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v) // "30s", "5m", "1h30m" — lesson 33
	if err != nil {
		return def, fmt.Errorf("%s: %q is not a duration (try 30s, 5m)", key, v)
	}
	return d, nil
}

func envCSV(key string, def []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------

func main() {
	fmt.Println("== 1. defaults only (nothing set) ==")
	show(Load(nil))

	fmt.Println()
	fmt.Println("== 2. env vars override defaults ==")
	setEnv(map[string]string{
		"APP_ENV":           "staging",
		"APP_PORT":          "9090",
		"APP_READ_TIMEOUT":  "45s",
		"APP_ENABLE_SIGNUP": "false",
		"APP_DB_DSN":        "postgres://user:hunter2@db.internal:5432/app",
		"APP_ALLOWED_HOSTS": "api.example.com, admin.example.com",
	})
	show(Load(nil))

	fmt.Println()
	fmt.Println("== 3. flags beat env vars ==")
	show(Load([]string{"-port", "7070", "-log-level", "debug"}))

	fmt.Println()
	fmt.Println("== 4. bad values are reported ALL AT ONCE ==")
	setEnv(map[string]string{
		"APP_PORT":         "8O80", // that's a letter O
		"APP_READ_TIMEOUT": "30",   // missing the unit
		"APP_DB_MAX_CONNS": "zero",
	})
	show(Load(nil))

	fmt.Println()
	fmt.Println("== 5. prod has stricter rules ==")
	clearEnv()
	setEnv(map[string]string{"APP_ENV": "prod"}) // no DSN, loopback host
	show(Load(nil))

	fmt.Println()
	fmt.Println("== 6. secrets never print ==")
	clearEnv()
	setEnv(map[string]string{"APP_DB_DSN": "postgres://user:hunter2@localhost/app"})
	cfg, _ := Load(nil)
	fmt.Printf("  %%v of the whole struct : %v\n", cfg.DB)
	fmt.Printf("  Printf the field       : %s\n", cfg.DB.DSN)
	fmt.Printf("  explicit Reveal()      : %s\n", cfg.DB.DSN.Reveal())
}

func show(c Config, err error) {
	if err != nil {
		fmt.Println("  CONFIG ERROR:")
		for _, line := range strings.Split(err.Error(), "\n") {
			fmt.Println("    -", line)
		}
		return
	}
	fmt.Printf("  env=%-8s addr=%-20s log=%-6s readTO=%-5v signup=%-5v conns=%d\n",
		c.Env, c.Addr(), c.LogLevel, c.Server.ReadTimeout, c.Features.EnableSignup, c.DB.MaxOpenConns)
	fmt.Printf("  dsn=%v hosts=%v\n", c.DB.DSN, c.Features.AllowedHosts)
}

func setEnv(kv map[string]string) {
	for k, v := range kv {
		os.Setenv(k, v)
	}
}

func clearEnv() {
	for _, k := range []string{
		"APP_ENV", "APP_LOG_LEVEL", "APP_HOST", "APP_PORT", "APP_READ_TIMEOUT",
		"APP_ENABLE_SIGNUP", "APP_DB_DSN", "APP_DB_MAX_CONNS", "APP_ALLOWED_HOSTS",
	} {
		os.Unsetenv(k)
	}
}

// ----------------------------------------------------------------------------
// RULES
//   1. ONE Config struct, built once in main, passed down explicitly. No
//      `config.Get()` global — that hides dependencies and wrecks tests.
//   2. Defaults <- env <- flags. Never read os.Getenv outside the loader.
//   3. Validate at STARTUP and exit non-zero. A service that boots with bad
//      config and fails on the first request is much worse than one that
//      refuses to boot.
//   4. Report every config problem at once (errors.Join). Operators hate
//      fixing one typo per restart.
//   5. Secrets: use a type that can't print itself. Never log the whole
//      Config struct without one.
//   6. Commit a .env.example listing every variable with PLACEHOLDER
//      values. Never commit .env itself.
//   7. Real secrets in production come from a secret manager (Vault, AWS
//      Secrets Manager, k8s Secrets) injected as env vars — the code above
//      doesn't change.
//
// WHEN TO REACH FOR A LIBRARY
//   The stdlib approach here is ~200 lines and has no dependencies, which
//   is why most Go services just write it. If you want tags instead
//   (`env:"APP_PORT" envDefault:"8080"`), use github.com/caarlos0/env or
//   github.com/kelseyhightower/envconfig. Use spf13/viper only if you
//   genuinely need config FILES with hot reload — it is a large dependency.
// ----------------------------------------------------------------------------

// Package config loads and validates the service's configuration from
// defaults, environment variables and flags — lesson 41 applied for real.
package config

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Secret is a string that will not print itself. Lesson 41.
type Secret string

func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "[REDACTED]"
}

func (s Secret) Reveal() string { return string(s) }

type Config struct {
	Env      string
	LogLevel string

	HTTP struct {
		Addr            string
		ReadTimeout     time.Duration
		WriteTimeout    time.Duration
		IdleTimeout     time.Duration
		ShutdownTimeout time.Duration
		RequestTimeout  time.Duration
	}

	DB struct {
		Path         string
		MaxOpenConns int
	}

	Auth struct {
		APIKey Secret
	}
}

// Load builds a validated Config. Pass os.Args[1:] in main, or a fixed
// slice in tests.
func Load(args []string) (Config, error) {
	var c Config

	// 1. defaults
	c.Env = "dev"
	c.LogLevel = "info"
	c.HTTP.Addr = "127.0.0.1:8080"
	c.HTTP.ReadTimeout = 10 * time.Second
	c.HTTP.WriteTimeout = 15 * time.Second
	c.HTTP.IdleTimeout = 60 * time.Second
	c.HTTP.ShutdownTimeout = 15 * time.Second
	c.HTTP.RequestTimeout = 5 * time.Second
	c.DB.Path = "tasks.db"
	c.DB.MaxOpenConns = 10
	c.Auth.APIKey = "dev-key"

	// 2. environment
	c.Env = envStr("APP_ENV", c.Env)
	c.LogLevel = envStr("APP_LOG_LEVEL", c.LogLevel)
	c.HTTP.Addr = envStr("APP_ADDR", c.HTTP.Addr)
	c.DB.Path = envStr("APP_DB_PATH", c.DB.Path)
	c.Auth.APIKey = Secret(envStr("APP_API_KEY", c.Auth.APIKey.Reveal()))

	var errs []error
	var err error
	if c.DB.MaxOpenConns, err = envInt("APP_DB_MAX_CONNS", c.DB.MaxOpenConns); err != nil {
		errs = append(errs, err)
	}
	if c.HTTP.RequestTimeout, err = envDur("APP_REQUEST_TIMEOUT", c.HTTP.RequestTimeout); err != nil {
		errs = append(errs, err)
	}
	if c.HTTP.ShutdownTimeout, err = envDur("APP_SHUTDOWN_TIMEOUT", c.HTTP.ShutdownTimeout); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}

	// 3. flags (highest precedence; each default is the current value)
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	fs.StringVar(&c.Env, "env", c.Env, "dev|staging|prod")
	fs.StringVar(&c.HTTP.Addr, "addr", c.HTTP.Addr, "listen address")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "debug|info|warn|error")
	fs.StringVar(&c.DB.Path, "db", c.DB.Path, "path to the SQLite database file")
	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}

	// 4. validate
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) Validate() error {
	var errs []error
	if !oneOf(c.Env, "dev", "staging", "prod") {
		errs = append(errs, fmt.Errorf("APP_ENV: %q must be dev|staging|prod", c.Env))
	}
	if _, err := c.SlogLevel(); err != nil {
		errs = append(errs, err)
	}
	if c.HTTP.Addr == "" {
		errs = append(errs, errors.New("APP_ADDR: must not be empty"))
	}
	if c.DB.Path == "" {
		errs = append(errs, errors.New("APP_DB_PATH: must not be empty"))
	}
	if c.DB.MaxOpenConns < 1 {
		errs = append(errs, fmt.Errorf("APP_DB_MAX_CONNS: must be >= 1, got %d", c.DB.MaxOpenConns))
	}
	if c.Env == "prod" && c.Auth.APIKey == "dev-key" {
		errs = append(errs, errors.New("APP_API_KEY: the default key must not be used in prod"))
	}
	return errors.Join(errs...)
}

// SlogLevel maps the configured level string to a slog.Level, and doubles
// as the validator for it.
func (c Config) SlogLevel() (slog.Level, error) {
	switch c.LogLevel {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("APP_LOG_LEVEL: %q is not debug|info|warn|error", c.LogLevel)
	}
}

// ---- typed env helpers (lesson 41) ----

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
		return def, fmt.Errorf("%s: %q is not an integer", key, v)
	}
	return n, nil
}

func envDur(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def, fmt.Errorf("%s: %q is not a duration (try 30s, 5m)", key, v)
	}
	return d, nil
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

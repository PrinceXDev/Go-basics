package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Rule struct {
	Limit  int
	Window time.Duration
}

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	// FailOpen decides what happens when the limiter's database is down.
	FailOpen bool
	Tenant   Rule
	APIKey   Rule
	User     Rule

	// Fault tolerance knobs, see internal/service.
	DBTimeout        time.Duration // per database attempt
	RetryAttempts    int           // total tries per check, including the first
	RetryBackoff     time.Duration // base delay, doubled each retry
	BreakerThreshold int           // consecutive failures before the breaker opens
	BreakerCooldown  time.Duration // how long it stays open before a probe
}

// Load reads configuration from environment variables and validates it,
// so a bad value stops the app at startup instead of failing under traffic.
func Load() (Config, error) {
	window, err := durationEnv("RATE_LIMIT_WINDOW", time.Minute)
	if err != nil {
		return Config{}, err
	}

	var errs []error
	intOf := func(key string, def int) int {
		v, err := intEnv(key, def)
		errs = append(errs, err)
		return v
	}

	cfg := Config{
		HTTPAddr:    stringEnv("HTTP_ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		Tenant:      Rule{Limit: intOf("RATE_LIMIT_TENANT", 100), Window: window},
		APIKey:      Rule{Limit: intOf("RATE_LIMIT_API_KEY", 40), Window: window},
		User:        Rule{Limit: intOf("RATE_LIMIT_USER", 20), Window: window},
	}

	failOpen, err := boolEnv("RATE_LIMIT_FAIL_OPEN", true)
	errs = append(errs, err)
	cfg.FailOpen = failOpen

	durOf := func(key string, def time.Duration) time.Duration {
		v, err := durationEnv(key, def)
		errs = append(errs, err)
		return v
	}
	cfg.DBTimeout = durOf("DB_TIMEOUT", 200*time.Millisecond)
	cfg.RetryAttempts = intOf("DB_RETRY_ATTEMPTS", 3)
	cfg.RetryBackoff = durOf("DB_RETRY_BACKOFF", 20*time.Millisecond)
	cfg.BreakerThreshold = intOf("BREAKER_THRESHOLD", 5)
	cfg.BreakerCooldown = durOf("BREAKER_COOLDOWN", 10*time.Second)

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if c.DBTimeout <= 0 {
		errs = append(errs, fmt.Errorf("DB_TIMEOUT must be > 0, got %s", c.DBTimeout))
	}
	if c.RetryAttempts < 1 {
		errs = append(errs, fmt.Errorf("DB_RETRY_ATTEMPTS must be >= 1, got %d", c.RetryAttempts))
	}
	if c.RetryBackoff < 0 {
		errs = append(errs, fmt.Errorf("DB_RETRY_BACKOFF must be >= 0, got %s", c.RetryBackoff))
	}
	if c.BreakerThreshold < 1 {
		errs = append(errs, fmt.Errorf("BREAKER_THRESHOLD must be >= 1, got %d", c.BreakerThreshold))
	}
	if c.BreakerCooldown <= 0 {
		errs = append(errs, fmt.Errorf("BREAKER_COOLDOWN must be > 0, got %s", c.BreakerCooldown))
	}
	for name, r := range map[string]Rule{"tenant": c.Tenant, "api_key": c.APIKey, "user": c.User} {
		if r.Limit <= 0 {
			errs = append(errs, fmt.Errorf("%s limit must be > 0, got %d", name, r.Limit))
		}
		// Windows are aligned to whole seconds in SQL, so sub-second values would break the math.
		if r.Window < time.Second {
			errs = append(errs, fmt.Errorf("%s window must be >= 1s, got %s", name, r.Window))
		}
	}
	return errors.Join(errs...)
}

func stringEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnv(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", key, v)
	}
	return n, nil
}

func boolEnv(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %q is not a boolean", key, v)
	}
	return b, nil
}

func durationEnv(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration (try 30s, 1m, 1h)", key, v)
	}
	return d, nil
}

package config

import "os"

// Config holds everything the app needs to boot, read once in main and
// passed down explicitly.
type Config struct {
	Port        string
	DatabaseURL string
}

// Load reads config from the environment, falling back to sane local
// defaults so `go run` works out of the box against a local Postgres.
func Load() Config {
	return Config{
		Port:        envOr("APP_PORT", "8080"),
		DatabaseURL: envOr("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/go_crud?sslmode=disable"),
	}
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

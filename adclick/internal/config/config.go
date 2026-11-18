// Package config loads settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr    string
	MetricsAddr string // consumer's /metrics server
	DatabaseURL string

	KafkaBrokers      []string
	TopicEvents       string
	TopicRetry        string
	TopicDLQ          string
	ConsumerGroup     string
	TopicPartitions   int32
	ReplicationFactor int16

	IPHashSalt string

	// Consumer tuning.
	ConsumerConcurrency int           // clients in the group inside one process
	BatchSize           int           // max records per DB transaction
	MaxRetries          int           // retry-topic hops before the DLQ
	RetryDelay          time.Duration // minimum wait before a retried event is reprocessed
	DedupRetention      time.Duration // must exceed Kafka retention
	MaxEventAge         time.Duration // reject clicks older than this
	MaxFutureSkew       time.Duration

	AdCacheRefresh time.Duration
	LogLevel       string
}

func Load() (Config, error) {
	c := Config{
		HTTPAddr:            env("HTTP_ADDR", ":8080"),
		MetricsAddr:         env("METRICS_ADDR", ":9101"),
		DatabaseURL:         env("DATABASE_URL", "postgres://adclick:adclick@localhost:5432/adclick?sslmode=disable"),
		KafkaBrokers:        strings.Split(env("KAFKA_BROKERS", "localhost:9092"), ","),
		TopicEvents:         env("TOPIC_EVENTS", "ad-click-events"),
		TopicRetry:          env("TOPIC_RETRY", "ad-click-retry"),
		TopicDLQ:            env("TOPIC_DLQ", "ad-click-dlq"),
		ConsumerGroup:       env("CONSUMER_GROUP", "click-aggregators"),
		TopicPartitions:     int32(envInt("TOPIC_PARTITIONS", 12)),
		ReplicationFactor:   int16(envInt("REPLICATION_FACTOR", 1)),
		IPHashSalt:          env("IP_HASH_SALT", "dev-only-salt"),
		ConsumerConcurrency: envInt("CONSUMER_CONCURRENCY", 2),
		BatchSize:           envInt("BATCH_SIZE", 500),
		MaxRetries:          envInt("MAX_RETRIES", 3),
		RetryDelay:          envDur("RETRY_DELAY", 5*time.Second),
		DedupRetention:      envDur("DEDUP_RETENTION", 8*24*time.Hour),
		MaxEventAge:         envDur("MAX_EVENT_AGE", 7*24*time.Hour),
		MaxFutureSkew:       envDur("MAX_FUTURE_SKEW", 5*time.Minute),
		AdCacheRefresh:      envDur("AD_CACHE_REFRESH", 30*time.Second),
		LogLevel:            env("LOG_LEVEL", "info"),
	}
	if c.BatchSize < 1 || c.ConsumerConcurrency < 1 || c.MaxRetries < 0 {
		return c, fmt.Errorf("invalid consumer settings")
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

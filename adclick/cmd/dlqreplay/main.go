// Command dlqreplay moves messages from the DLQ back to the main topic.
// Fix the root cause first: a still-invalid message just returns to the DLQ.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"adclick/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cl, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.KafkaBrokers...),
		kgo.ConsumerGroup(cfg.ConsumerGroup+"-dlq-replay"),
		kgo.ConsumeTopics(cfg.TopicDLQ),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		kgo.RequiredAcks(kgo.AllISRAcks()),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer cl.Close()

	total := 0
	for {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		fetches := cl.PollFetches(pctx)
		cancel()
		recs := fetches.Records()
		if len(recs) == 0 {
			break // idle for 5s: DLQ drained
		}
		for _, r := range recs {
			res := cl.ProduceSync(ctx, &kgo.Record{Topic: cfg.TopicEvents, Key: r.Key, Value: r.Value})
			if err := res.FirstErr(); err != nil {
				log.Fatalf("republish failed, offsets not committed: %v", err)
			}
			total++
		}
		if err := cl.CommitRecords(ctx, recs...); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf("replayed %d messages from %s to %s\n", total, cfg.TopicDLQ, cfg.TopicEvents)
}

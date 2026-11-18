package main

// ASYNC COMMUNICATION — an in-memory stand-in for Kafka / RabbitMQ.
//
// Sync (HTTP/gRPC):  "debit this account and tell me if it worked" — caller WAITS.
// Async (events):    "a transfer completed" — publisher does NOT wait and does
//                    not even know who is listening (SMS, fraud scoring,
//                    statements, loyalty points). Adding a new listener needs
//                    zero changes to payments: that is loose coupling.
//
// The price: delivery is at-least-once and eventually consistent. The SMS
// may arrive a second after the API response, and may be delivered twice.

import (
	"context"
	"log/slog"
	"sync"
)

type Event struct {
	ID        string   `json:"id"` // consumers dedupe on this
	Type      string   `json:"type"`
	RequestID string   `json:"request_id"` // tracing continues across the async hop
	Transfer  Transfer `json:"transfer"`
}

type Bus struct {
	mu   sync.RWMutex
	subs map[string][]chan Event
}

func NewBus() *Bus { return &Bus{subs: map[string][]chan Event{}} }

func (b *Bus) Subscribe(topic string) <-chan Event {
	ch := make(chan Event, 100)
	b.mu.Lock()
	b.subs[topic] = append(b.subs[topic], ch)
	b.mu.Unlock()
	return ch
}

func (b *Bus) Publish(topic string, e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs[topic] {
		ch <- e
	}
}

// RunNotifications is an IDEMPOTENT CONSUMER: it remembers processed event
// IDs (the "inbox" pattern; a DB table in production) so a redelivered event
// doesn't send the customer a second SMS.
func RunNotifications(ctx context.Context, events <-chan Event, log *slog.Logger) {
	seen := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-events:
			if seen[e.ID] {
				log.Info("notifications: duplicate ignored", "event", e.ID)
				continue
			}
			seen[e.ID] = true
			t := e.Transfer
			log.Info("notifications: SMS sent", "rid", e.RequestID, "type", e.Type,
				"text", inr(t.Amount)+" "+t.From+" -> "+t.To+" "+t.Status)
		}
	}
}

package payment

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Event is a human-readable line for the dashboard timeline. It's an
// in-memory ring buffer — fine for a demo, not an audit log.
type Event struct {
	Seq     int64     `json:"seq"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"` // breaker | sync | retry | fallback | worker | dlq
	OrderID string    `json:"orderId,omitempty"`
	Message string    `json:"message"`
}

type EventLog struct {
	mu     sync.Mutex
	seq    int64
	events []Event
	max    int
}

func NewEventLog(max int) *EventLog { return &EventLog{max: max} }

func (l *EventLog) Add(kind, orderID, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	slog.Info(msg, "kind", kind, "order", orderID)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	l.events = append(l.events, Event{Seq: l.seq, At: time.Now().UTC(), Kind: kind, OrderID: orderID, Message: msg})
	if len(l.events) > l.max {
		l.events = l.events[len(l.events)-l.max:]
	}
}

// Recent returns newest first.
func (l *EventLog) Recent(n int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > len(l.events) {
		n = len(l.events)
	}
	out := make([]Event, 0, n)
	for i := len(l.events) - 1; i >= len(l.events)-n; i-- {
		out = append(out, l.events[i])
	}
	return out
}

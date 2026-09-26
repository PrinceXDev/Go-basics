// Package hub fans one stream of updates out to many WebSocket clients
// (dashboard browser tabs). This is the "share memory by communicating"
// half of the demo: instead of every client goroutine reaching into a
// shared map of connections behind a mutex, everything funnels through
// channels owned by a single Run loop goroutine.
package hub

import (
	"context"
	"log/slog"

	"github.com/gorilla/websocket"
)

// Client wraps one dashboard's WebSocket connection. send is buffered so a
// momentarily slow browser tab doesn't block the broadcaster; if the buffer
// fills up anyway, the hub drops that client rather than let one slow
// reader stall updates for everyone else (see Run's select/default below).
type Client struct {
	conn *websocket.Conn
	send chan []byte
}

// Hub owns the client set and the broadcast channel. All mutation of the
// client set happens inside Run, on one goroutine — so the set itself needs
// no mutex at all.
type Hub struct {
	clients    map[*Client]struct{}
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
}

func New() *Hub {
	return &Hub{
		clients:    make(map[*Client]struct{}),
		broadcast:  make(chan []byte, 64),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Broadcast queues a message for every connected client. Safe to call from
// any goroutine (e.g. an HTTP handler that just ingested a health report).
func (h *Hub) Broadcast(msg []byte) {
	h.broadcast <- msg
}

// Run is the hub's single event loop. Call it once, in its own goroutine.
// It exits when ctx is cancelled, closing every client connection on the
// way out so their write pumps unblock and return.
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			for c := range h.clients {
				close(c.send)
			}
			return

		case c := <-h.register:
			h.clients[c] = struct{}{}
			slog.Info("dashboard connected", "clients", len(h.clients))

		case c := <-h.unregister:
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
				slog.Info("dashboard disconnected", "clients", len(h.clients))
			}

		case msg := <-h.broadcast:
			for c := range h.clients {
				select {
				case c.send <- msg:
				default:
					// c's buffer is full: it's not keeping up. Drop it
					// instead of blocking the whole broadcast on one slow
					// reader.
					delete(h.clients, c)
					close(c.send)
				}
			}
		}
	}
}

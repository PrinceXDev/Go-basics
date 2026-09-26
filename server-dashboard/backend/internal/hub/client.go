package hub

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10 // must stay well under pongWait
)

var upgrader = websocket.Upgrader{
	// Demo runs frontend (Vite, :5173) and backend (:8080) on different
	// origins, so the default same-origin check would reject it. A real
	// deployment would check r.Header.Get("Origin") against an allowlist.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// ServeWS upgrades an HTTP request to a WebSocket, sends the current
// snapshot so the dashboard has data immediately (it doesn't have to wait
// for the next health report), then registers the client with the hub and
// starts its read/write pumps.
func ServeWS(h *Hub, w http.ResponseWriter, r *http.Request, initial []byte) error {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}

	c := &Client{conn: conn, send: make(chan []byte, 16)}
	h.register <- c

	if len(initial) > 0 {
		c.send <- initial
	}

	go c.writePump()
	go c.readPump(h)

	return nil
}

// writePump is the ONLY goroutine that ever writes to this connection —
// gorilla/websocket connections are not safe for concurrent writes, so
// every outbound message must flow through this one channel/goroutine per
// client. It also sends periodic pings so a dead TCP connection (laptop
// went to sleep, wifi dropped) gets noticed instead of leaking forever.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// hub closed our channel: we've been unregistered.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// readPump doesn't expect any messages from the dashboard (it's a
// read-only view) — its only job is to detect when the browser tab closes
// so we can unregister the client and free its resources. A closed
// connection makes conn.ReadMessage return an error, which is our signal.
func (c *Client) readPump(h *Hub) {
	defer func() {
		h.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Debug("websocket read error", "err", err)
			}
			return
		}
	}
}

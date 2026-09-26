import { useEffect, useRef, useState } from "react";

const WS_URL = "ws://localhost:8080/ws";
const REST_URL = "http://localhost:8080/api/servers";

/**
 * Owns the live connection to the Go dashboard backend.
 *
 * Why a Map keyed by server id, not an array: updates arrive one server at
 * a time ({"type":"update","server":{...}}), so merging into a keyed
 * collection is O(1) and never touches the other 9 servers' rows. We only
 * flatten to an array (sorted) for rendering, in the return value.
 *
 * Why manual reconnect-with-backoff instead of a library: this is the one
 * piece of "real" frontend complexity in the demo, and it's worth seeing
 * plainly — a WebSocket that closes (backend restarts, wifi blip) needs a
 * timer-based retry, capped so a dead backend doesn't spin the tab.
 */
export function useServerFeed() {
  const [servers, setServers] = useState(new Map());
  const [connectionState, setConnectionState] = useState("connecting"); // connecting | open | closed
  const retryDelay = useRef(500);

  useEffect(() => {
    let socket;
    let retryTimer;
    let cancelled = false;

    async function loadInitialSnapshot() {
      try {
        const res = await fetch(REST_URL);
        if (!res.ok) return;
        const list = await res.json();
        if (cancelled || !Array.isArray(list)) return;
        setServers(new Map(list.map((s) => [s.id, s])));
      } catch {
        // WebSocket's own snapshot message will fill this in instead.
      }
    }

    function connect() {
      setConnectionState("connecting");
      socket = new WebSocket(WS_URL);

      socket.onopen = () => {
        retryDelay.current = 500; // reset backoff after a healthy connection
        setConnectionState("open");
      };

      socket.onmessage = (event) => {
        const msg = JSON.parse(event.data);
        if (msg.type === "snapshot") {
          setServers(new Map(msg.servers.map((s) => [s.id, s])));
        } else if (msg.type === "update") {
          setServers((prev) => {
            const next = new Map(prev);
            next.set(msg.server.id, msg.server);
            return next;
          });
        }
      };

      socket.onclose = () => {
        if (cancelled) return;
        setConnectionState("closed");
        retryTimer = setTimeout(connect, retryDelay.current);
        retryDelay.current = Math.min(retryDelay.current * 2, 8000);
      };

      socket.onerror = () => socket.close();
    }

    loadInitialSnapshot();
    connect();

    return () => {
      cancelled = true;
      clearTimeout(retryTimer);
      socket?.close();
    };
  }, []);

  const sorted = [...servers.values()].sort((a, b) => a.id.localeCompare(b.id));
  return { servers: sorted, connectionState };
}

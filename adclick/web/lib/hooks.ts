"use client";
import { useEffect, useRef, useState } from "react";
import type { Summary } from "./api";

/** Polls fn every `ms`. Keeps the last good data if a refresh fails. */
export function usePolling<T>(fn: () => Promise<T>, ms: number, deps: unknown[]) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const fnRef = useRef(fn);
  fnRef.current = fn;

  useEffect(() => {
    let stop = false;
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      try {
        const d = await fnRef.current();
        if (!stop) {
          setData(d);
          setError(null);
        }
      } catch (e) {
        if (!stop) setError(e instanceof Error ? e.message : "failed");
      }
      if (!stop) timer = setTimeout(tick, ms);
    };
    tick();
    return () => {
      stop = true;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  return { data, error };
}

/** Live last-hour summary over Server-Sent Events (browser auto-reconnects). */
export function useLiveSummary() {
  const [live, setLive] = useState<Summary | null>(null);
  const [connected, setConnected] = useState(false);
  useEffect(() => {
    const es = new EventSource("/api/v1/stream");
    es.onopen = () => setConnected(true);
    es.onmessage = (m) => setLive(JSON.parse(m.data));
    es.onerror = () => setConnected(false);
    return () => es.close();
  }, []);
  return { live, connected };
}

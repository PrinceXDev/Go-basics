"use client";

import { useCallback, useEffect, useState } from "react";
import { api, money, type BreakerSnapshot, type GatewayMode, type Payment, type State } from "@/lib/api";

const MODES: { mode: GatewayMode; label: string; help: string }[] = [
  { mode: "healthy", label: "Healthy", help: "Fast and always works." },
  { mode: "flaky", label: "Flaky", help: "Some calls fail with 503. Retries usually save them." },
  { mode: "slow", label: "Slow", help: "Charges the card but replies after 2.5s. Our 1s timeout gives up, so the outcome is unknown." },
  { mode: "down", label: "Down", help: "Every call fails with 503. The breaker opens and payments are queued." },
];

type Banner = { tone: "good" | "warn" | "bad" | "info"; title: string; body: string; meta?: string };

export default function Page() {
  const [state, setState] = useState<State | null>(null);
  const [payments, setPayments] = useState<Payment[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [amount, setAmount] = useState(49.99);
  const [flakyRate, setFlakyRate] = useState(50);
  const [busy, setBusy] = useState(false);
  const [banner, setBanner] = useState<Banner | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [s, p] = await Promise.all([api.state(), api.payments()]);
      setState(s);
      setPayments(p);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    refresh();
    const id = setInterval(refresh, 1000);
    return () => clearInterval(id);
  }, [refresh]);

  async function run<T>(fn: () => Promise<T>) {
    setBusy(true);
    try {
      return await fn();
    } catch (e) {
      setBanner({ tone: "bad", title: "Request failed", body: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
      refresh();
    }
  }

  const pay = () =>
    run(async () => {
      const started = performance.now();
      const r = await api.pay(amount);
      const ms = Math.round(performance.now() - started);
      const meta = `HTTP ${r.httpStatus} · ${ms} ms · ${r.payment.orderId} · sync attempts: ${r.payment.syncAttempts}`;
      const map: Record<string, Banner> = {
        succeeded: { tone: "good", title: "Payment successful", body: `Charged ${money(r.payment.amountCents)} (${r.payment.gatewayChargeId}).`, meta },
        queued: { tone: "warn", title: "Order placed: payment processing", body: "The gateway is unavailable, so we saved your order and will finish the payment in the background. The user does not see an error page.", meta },
        declined: { tone: "bad", title: "Card declined", body: "A business error, not an outage, so it is not retried and does not count against the breaker.", meta },
        existing: { tone: "info", title: "Duplicate request", body: "This order already exists, so the earlier result is returned.", meta },
      };
      setBanner(map[r.outcome]);
    });

  const burst = () =>
    run(async () => {
      const r = await api.burst(20, amount);
      const parts = Object.entries(r.outcomes).map(([k, v]) => `${v} ${k}`).join(", ");
      setBanner({ tone: "info", title: "Burst of 20 payments finished", body: parts || "nothing", meta: `took ${r.tookMs} ms with 5 concurrent users` });
    });

  const lookup = (orderId: string) =>
    run(async () => {
      const r = await api.lookup(orderId);
      const fromCache = r.source.startsWith("cache");
      setBanner({
        tone: fromCache ? "warn" : "info",
        title: fromCache ? "Served from cache (fallback)" : "Fresh from gateway",
        body: `${orderId} is ${r.payment.status}.`,
        meta: `source: ${r.source}`,
      });
    });

  const setMode = (mode: GatewayMode) => run(() => api.setGateway(mode, flakyRate));

  const reset = () =>
    run(async () => {
      await api.reset();
      setBanner(null);
    });

  const mode = state?.gateway.settings.mode;

  return (
    <main className="wrap">
      <header>
        <div>
          <h1>Payment circuit breaker demo</h1>
          <div className="sub">
            Next.js → Go payment API → <b>Retry(CircuitBreaker(gateway call))</b>. If that fails, the fallback saves the
            payment in a PostgreSQL retry queue, and a background worker finishes it later.
          </div>
        </div>
        <button className="btn" onClick={reset} disabled={busy}>Clear demo data</button>
      </header>

      {error && <div className="msg bad"><b>Cannot reach the Go API</b>{error}. Is <span className="mono">go run ./cmd/api</span> running?</div>}

      <section className="grid g2">
        <div className="card">
          <h2>1 · External payment gateway (simulated)</h2>
          <div className="row">
            {MODES.map((m) => (
              <button key={m.mode} className={`btn ${mode === m.mode ? "on" : ""}`} onClick={() => setMode(m.mode)} disabled={busy}>
                {m.label}
              </button>
            ))}
          </div>
          <div className="row mt">
            <label className="hint" style={{ marginTop: 0 }}>Flaky failure rate: {flakyRate}%</label>
            <input type="range" min={10} max={100} step={10} value={flakyRate} onChange={(e) => setFlakyRate(+e.target.value)} />
          </div>
          <div className="hint">{MODES.find((m) => m.mode === mode)?.help ?? "…"}</div>
          {state && (
            <div className="tiles mt">
              <Tile v={state.gateway.stats.requests} l="requests received" />
              <Tile v={state.gateway.stats.rejected503} l="503s returned" />
              <Tile v={state.gateway.stats.chargesCaptured} l="cards charged" />
              <Tile v={state.gateway.stats.duplicatesBlocked} l="double charges blocked" />
            </div>
          )}
        </div>

        <div className="card">
          <h2>2 · Checkout</h2>
          <div className="row">
            <input type="number" min={1} step={0.01} value={amount} onChange={(e) => setAmount(+e.target.value)} aria-label="Amount" />
            <button className="btn primary" onClick={pay} disabled={busy || amount <= 0}>Pay {amount.toFixed(2)}</button>
            <button className="btn" onClick={burst} disabled={busy}>Burst × 20</button>
          </div>
          <div className="hint">Amounts above 500.00 are declined by the gateway (business error).</div>
          {banner && (
            <div className={`msg ${banner.tone}`}>
              <b>{banner.title}</b>
              {banner.body}
              {banner.meta && <small className="mono">{banner.meta}</small>}
            </div>
          )}
        </div>
      </section>

      {state && (
        <section className="grid g2 mt">
          <div className="card">
            <h2>3 · Circuit breaker: {state.breaker.name}</h2>
            <BreakerDiagram b={state.breaker} />
            <BreakerMetrics b={state.breaker} />
          </div>

          <div className="card">
            <h2>4 · Payments &amp; retry queue (PostgreSQL)</h2>
            <div className="tiles">
              <Tile v={state.payments.SUCCEEDED} l="succeeded" />
              <Tile v={state.payments.QUEUED} l="queued" />
              <Tile v={state.payments.FAILED} l="failed" />
              <Tile v={state.payments.PENDING} l="in flight" />
            </div>
            <div className="tiles mt">
              <Tile v={state.queue.pending} l="jobs waiting" />
              <Tile v={state.queue.processing} l="jobs running" />
              <Tile v={state.queue.done} l="jobs done" />
              <Tile v={state.queue.dead} l="dead letters" />
            </div>
            <h2 className="mt">Try these</h2>
            <ol className="steps">
              <li><b>Happy path:</b> set Healthy, then Pay.</li>
              <li><b>Retries:</b> set Flaky at 50% and Pay a few times. Watch the timeline for retries.</li>
              <li><b>Breaker trips:</b> set Down, then Burst. The first calls fail, the breaker opens, and the rest fail fast and go to the queue.</li>
              <li><b>Recovery:</b> set Healthy and wait 10s. The breaker goes OPEN → HALF_OPEN → CLOSED and the worker clears the queue.</li>
              <li><b>Idempotency:</b> set Slow, then Pay. The call times out, but the card was charged. Set Healthy: the retry gets back the stored charge, so no double charge happens.</li>
              <li><b>Dead letter:</b> set Down, Pay, and wait about a minute. After 6 async tries the job goes to the dead-letter queue.</li>
              <li><b>Cached read:</b> set Down, then click a row&apos;s “check”. You get the last known status from the DB.</li>
            </ol>
          </div>
        </section>
      )}

      <section className="grid g2 mt">
        <div className="card">
          <h2>Recent payments</h2>
          <div className="tablewrap">
            <table>
              <thead>
                <tr><th>Order</th><th>Amount</th><th>Status</th><th>Path</th><th>Tries</th><th>Charge</th><th>Why</th><th /></tr>
              </thead>
              <tbody>
                {payments.length === 0 && <tr><td colSpan={8} className="hint">No payments yet. Click Pay.</td></tr>}
                {payments.map((p) => (
                  <tr key={p.orderId}>
                    <td className="mono">{p.orderId}</td>
                    <td>{money(p.amountCents)}</td>
                    <td><span className={`chip ${p.status}`}>{p.status}</span></td>
                    <td>{p.path}</td>
                    <td title="sync attempts + async attempts">{p.syncAttempts}{p.asyncAttempts != null ? ` + ${p.asyncAttempts}` : ""}</td>
                    <td className="mono">{p.gatewayChargeId ?? "—"}</td>
                    <td className="reason">{jobNote(p) ?? p.failureReason ?? ""}</td>
                    <td><button className="link" onClick={() => lookup(p.orderId)}>check</button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>

        <div className="card">
          <h2>Timeline</h2>
          <ul className="events">
            {state?.events.map((e) => (
              <li key={e.seq}>
                <time>{new Date(e.at).toLocaleTimeString([], { hour12: false })}</time>
                <span className={`kind ${e.kind}`}>{e.kind}</span>
                <span>{e.orderId && <span className="mono">{e.orderId} · </span>}{e.message}</span>
              </li>
            ))}
          </ul>
        </div>
      </section>
    </main>
  );
}

function jobNote(p: Payment): string | null {
  if (p.status !== "QUEUED" || !p.nextAttemptAt) return null;
  const s = Math.max(0, Math.round((new Date(p.nextAttemptAt).getTime() - Date.now()) / 1000));
  return `retry job ${p.jobStatus}; next try in ~${s}s (last: ${p.failureReason ?? "?"})`;
}

function Tile({ v, l }: { v: number; l: string }) {
  return (
    <div className="tile">
      <div className="v">{v}</div>
      <div className="l">{l}</div>
    </div>
  );
}

function BreakerDiagram({ b }: { b: BreakerSnapshot }) {
  const cls = (s: BreakerSnapshot["state"], extra: string) => `node ${extra} ${b.state === s ? "active" : ""}`;
  const waitS = b.config.waitDurationInOpenStateMs / 1000;
  return (
    <svg className="fsm" viewBox="0 0 520 250" role="img" aria-label={`Circuit breaker is ${b.state}`}>
      <defs>
        <marker id="arr" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L10,5 L0,10 z" />
        </marker>
      </defs>
      <path className="edge" d="M170,55 L348,55" markerEnd="url(#arr)" />
      <text className="elabel" x="259" y="45" textAnchor="middle">failure rate ≥ {b.config.failureRateThreshold}%</text>
      <path className="edge" d="M395,80 L292,188" markerEnd="url(#arr)" />
      <text className="elabel" x="328" y="122" textAnchor="end">after {waitS}s</text>
      <path className="edge" d="M318,188 L420,80" markerEnd="url(#arr)" />
      <text className="elabel" x="386" y="150" textAnchor="start">trial fails</text>
      <path className="edge" d="M210,188 L115,80" markerEnd="url(#arr)" />
      <text className="elabel" x="150" y="150" textAnchor="end">{b.config.permittedCallsInHalfOpen} trials pass</text>

      <g className={cls("CLOSED", "closed")}>
        <rect x="30" y="30" width="140" height="50" rx="10" />
        <text x="100" y="60" textAnchor="middle">CLOSED</text>
      </g>
      <g className={cls("OPEN", "open")}>
        <rect x="350" y="30" width="140" height="50" rx="10" />
        <text x="420" y="60" textAnchor="middle">OPEN</text>
      </g>
      <g className={cls("HALF_OPEN", "half")}>
        <rect x="190" y="190" width="140" height="50" rx="10" />
        <text x="260" y="220" textAnchor="middle">HALF_OPEN</text>
      </g>
    </svg>
  );
}

function BreakerMetrics({ b }: { b: BreakerSnapshot }) {
  const explain = {
    CLOSED: "Calls go through. The breaker tracks failures over the last few calls.",
    OPEN: `Calls are rejected right away without touching the gateway. Trial calls start in ${(b.openRemainingMs / 1000).toFixed(1)}s.`,
    HALF_OPEN: `Letting ${b.config.permittedCallsInHalfOpen} trial calls through (${b.halfOpenFinished}/${b.config.permittedCallsInHalfOpen} done). Everyone else is still rejected.`,
  }[b.state];
  return (
    <>
      <div className="hint" style={{ marginTop: 0 }}>{explain}</div>
      <div className="mt">
        <div className="row" style={{ justifyContent: "space-between", fontSize: 12 }}>
          <span>
            {b.state === "HALF_OPEN"
              ? `Failure rate of trial calls (${b.bufferedCalls}/${b.config.permittedCallsInHalfOpen})`
              : `Failure rate (last ${b.bufferedCalls}/${b.config.slidingWindowSize} calls)`}
          </span>
          <b>{b.failureRate.toFixed(0)}%</b>
        </div>
        <div className="bar mt" style={{ marginTop: 6 }}>
          <span style={{ width: `${Math.min(100, b.failureRate)}%` }} />
          <i style={{ left: `${b.config.failureRateThreshold}%` }} title="threshold" />
        </div>
      </div>
      <dl className="kv">
        <dt>Failed / recorded</dt><dd>{b.failedCalls} / {b.bufferedCalls} (needs ≥ {b.config.minimumNumberOfCalls} calls before judging)</dd>
        <dt>Slow calls</dt><dd>{b.slowCalls} (≥ {b.config.slowCallDurationThresholdMs} ms)</dd>
        <dt>Rejected (fail-fast)</dt><dd>{b.notPermittedCalls}</dd>
      </dl>
    </>
  );
}

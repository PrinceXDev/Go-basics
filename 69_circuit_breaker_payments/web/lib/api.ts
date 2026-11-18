// Types mirror the Go JSON (internal/store, internal/breaker, internal/gateway).

export type PaymentStatus = "PENDING" | "SUCCEEDED" | "QUEUED" | "FAILED";
export type Outcome = "succeeded" | "queued" | "declined" | "existing";
export type GatewayMode = "healthy" | "flaky" | "slow" | "down";

export interface Payment {
  orderId: string;
  amountCents: number;
  status: PaymentStatus;
  path: "sync" | "async";
  gatewayChargeId: string | null;
  failureReason: string | null;
  syncAttempts: number;
  asyncAttempts: number | null;
  jobStatus: "pending" | "processing" | "done" | "dead" | null;
  nextAttemptAt: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface BreakerSnapshot {
  name: string;
  state: "CLOSED" | "OPEN" | "HALF_OPEN";
  failureRate: number;
  slowCallRate: number;
  bufferedCalls: number;
  failedCalls: number;
  slowCalls: number;
  notPermittedCalls: number;
  openRemainingMs: number;
  halfOpenTrials: number;
  halfOpenFinished: number;
  config: {
    slidingWindowSize: number;
    minimumNumberOfCalls: number;
    failureRateThreshold: number;
    slowCallDurationThresholdMs: number;
    waitDurationInOpenStateMs: number;
    permittedCallsInHalfOpen: number;
  };
}

export interface EventItem {
  seq: number;
  at: string;
  kind: string;
  orderId?: string;
  message: string;
}

export interface State {
  breaker: BreakerSnapshot;
  payments: Record<PaymentStatus, number>;
  queue: { pending: number; processing: number; done: number; dead: number };
  gateway: {
    settings: { mode: GatewayMode; failureRate: number; slowDelayMs: number };
    stats: { requests: number; rejected503: number; chargesCaptured: number; duplicatesBlocked: number };
  };
  gatewayError?: string;
  events: EventItem[];
}

export interface PayResult {
  outcome: Outcome;
  payment: Payment;
  message: string;
}

async function json<T>(res: Response): Promise<T> {
  const body = await res.json().catch(() => ({}));
  if (!res.ok && res.status !== 402) {
    throw new Error((body as { error?: string }).error ?? `HTTP ${res.status}`);
  }
  return body as T;
}

export const api = {
  state: () => fetch("/api/state", { cache: "no-store" }).then((r) => json<State>(r)),
  payments: () => fetch("/api/payments?limit=40", { cache: "no-store" }).then((r) => json<Payment[]>(r)),
  pay: (amount: number, orderId?: string) =>
    fetch("/api/payments", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ amount, orderId }),
    }).then(async (r) => ({ httpStatus: r.status, ...(await json<PayResult>(r)) })),
  lookup: (orderId: string) =>
    fetch(`/api/payments/${encodeURIComponent(orderId)}`, { cache: "no-store" }).then((r) =>
      json<{ payment: Payment; source: string }>(r),
    ),
  setGateway: (mode: GatewayMode, failureRate?: number) =>
    fetch("/api/gateway", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode, failureRate }),
    }).then((r) => json<unknown>(r)),
  burst: (count: number, amount: number) =>
    fetch("/api/demo/burst", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ count, amount }),
    }).then((r) => json<{ outcomes: Record<string, number>; tookMs: number }>(r)),
  reset: () => fetch("/api/demo/reset", { method: "POST" }),
};

export const money = (cents: number) => (cents / 100).toFixed(2);

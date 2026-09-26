import { useEffect, useState } from "react";

const STATUS_LABEL = {
  healthy: "Healthy",
  degraded: "Degraded",
  down: "Down",
};

function timeAgo(isoString) {
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(isoString)) / 1000));
  if (seconds < 2) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  return `${Math.floor(seconds / 60)}m ago`;
}

function Meter({ label, value }) {
  const level = value > 85 ? "high" : value > 65 ? "medium" : "low";
  return (
    <div className="meter">
      <div className="meter-label">
        <span>{label}</span>
        <span>{value.toFixed(1)}%</span>
      </div>
      <div className="meter-track">
        <div className={`meter-fill meter-fill--${level}`} style={{ width: `${value}%` }} />
      </div>
    </div>
  );
}

export default function ServerCard({ server }) {
  // A local re-render tick so "12s ago" keeps counting up between
  // WebSocket messages, instead of freezing until the next update arrives.
  const [, setTick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 1000);
    return () => clearInterval(id);
  }, []);

  return (
    <div className={`server-card server-card--${server.status}`}>
      <div className="server-card__header">
        <span className="server-card__id">{server.id}</span>
        <span className={`status-pill status-pill--${server.status}`}>
          {STATUS_LABEL[server.status] ?? server.status}
        </span>
      </div>

      <Meter label="CPU" value={server.cpuPercent} />
      <Meter label="Memory" value={server.memPercent} />

      <div className="server-card__footer">
        <span>{server.requestRate.toFixed(0)} req/s</span>
        <span>{timeAgo(server.updatedAt)}</span>
      </div>
    </div>
  );
}

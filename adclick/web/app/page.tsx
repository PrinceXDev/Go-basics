"use client";

import { useMemo, useState } from "react";
import {
  Area, AreaChart, Bar, BarChart, CartesianGrid, Cell, Pie, PieChart,
  ResponsiveContainer, Tooltip, XAxis, YAxis,
} from "recharts";
import {
  autoGranularity, getJSON, rangeParams,
  type AdStat, type CampaignStat, type Granularity, type Point, type Range, type Slice, type Summary,
} from "@/lib/api";
import { useLiveSummary, usePolling } from "@/lib/hooks";

const PRESETS = [
  { label: "1h", ms: 3_600_000 },
  { label: "24h", ms: 86_400_000 },
  { label: "7d", ms: 7 * 86_400_000 },
  { label: "30d", ms: 30 * 86_400_000 },
];
const COLORS = ["#4f46e5", "#06b6d4", "#f59e0b", "#10b981", "#ef4444", "#8b5cf6"];
const fmt = (n: number) => n.toLocaleString("en-US");
const pct = (n: number) => (n * 100).toFixed(2) + "%";

export default function Dashboard() {
  const [preset, setPreset] = useState("24h");
  const [custom, setCustom] = useState<{ from: string; to: string }>({ from: "", to: "" });
  const [gran, setGran] = useState<Granularity | "auto">("auto");
  const [tick, setTick] = useState(0); // changes when the user picks a new range

  // Presets are relative to "now" at each poll, so the window slides forward.
  const getRange = (): Range => {
    if (preset === "custom" && custom.from && custom.to) {
      return { from: new Date(custom.from), to: new Date(custom.to) };
    }
    const p = PRESETS.find((x) => x.label === preset) ?? PRESETS[1];
    const now = Date.now();
    return { from: new Date(now - p.ms), to: new Date(now + 60_000) };
  };
  const deps = [preset, custom.from, custom.to, gran, tick];

  const summary = usePolling(() => getJSON<Summary>("stats/summary", rangeParams(getRange())), 3000, deps);
  const series = usePolling(() => {
    const r = getRange();
    return getJSON<Point[]>("stats/timeseries", { ...rangeParams(r), granularity: gran === "auto" ? autoGranularity(r) : gran });
  }, 3000, deps);
  const ads = usePolling(() => getJSON<AdStat[]>("stats/top-ads", { ...rangeParams(getRange()), limit: "8" }), 5000, deps);
  const camps = usePolling(() => getJSON<CampaignStat[]>("stats/campaigns", rangeParams(getRange())), 5000, deps);
  const countries = usePolling(() => getJSON<Slice[]>("stats/countries", rangeParams(getRange())), 5000, deps);
  const devices = usePolling(() => getJSON<Slice[]>("stats/devices", rangeParams(getRange())), 5000, deps);
  const { live, connected } = useLiveSummary();

  const failing = [summary, series, ads, camps, countries, devices].some((q) => q.error);
  const effGran = useMemo(() => (gran === "auto" ? autoGranularity(getRange()) : gran), [gran, preset, custom, tick]); // eslint-disable-line react-hooks/exhaustive-deps
  const s = summary.data;

  return (
    <div className="wrap">
      <header>
        <div>
          <h1>Ad Click Analytics</h1>
          <div className="sub">
            <span className="live"><span className={`dot ${connected ? "on" : ""}`} />{connected ? "Live" : "Reconnecting…"}</span>
            {" · "}refreshes every 3s
          </div>
        </div>
        <div className="controls">
          {PRESETS.map((p) => (
            <button key={p.label} className={`btn ${preset === p.label ? "active" : ""}`} onClick={() => { setPreset(p.label); setTick((t) => t + 1); }}>
              {p.label}
            </button>
          ))}
          <input type="datetime-local" aria-label="From" value={custom.from} onChange={(e) => { setCustom({ ...custom, from: e.target.value }); setPreset("custom"); }} />
          <input type="datetime-local" aria-label="To" value={custom.to} onChange={(e) => { setCustom({ ...custom, to: e.target.value }); setPreset("custom"); }} />
          <select aria-label="Granularity" value={gran} onChange={(e) => setGran(e.target.value as Granularity | "auto")}>
            <option value="auto">Auto bucket</option>
            <option value="minute">Per minute</option>
            <option value="hour">Per hour</option>
            <option value="day">Per day</option>
          </select>
        </div>
      </header>

      {failing && <div className="banner">Some data could not be refreshed. Showing the last good values.</div>}

      <div className="grid kpis">
        <Kpi title="Total clicks" value={s ? fmt(s.total_clicks) : "—"} hint="in selected range" />
        <Kpi title="Unique clicks" value={s ? fmt(s.unique_clicks) : "—"} hint="first click per visitor, ad and day" />
        <Kpi title="Clicks / minute" value={live ? live.clicks_per_minute.toFixed(1) : s ? s.clicks_per_minute.toFixed(1) : "—"} hint="last 5 minutes (live)" />
        <Kpi title="CTR" value={s && s.impressions > 0 ? pct(s.ctr) : "n/a"} hint={s ? `${fmt(s.impressions)} impressions` : ""} />
      </div>

      <div className="card">
        <h2>Click trend ({effGran})</h2>
        {series.data && series.data.length > 0 ? (
          <ResponsiveContainer width="100%" height={280}>
            <AreaChart data={series.data}>
              <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
              <XAxis dataKey="bucket" tickFormatter={(v) => tickLabel(v, effGran)} stroke="var(--muted)" fontSize={12} />
              <YAxis stroke="var(--muted)" fontSize={12} width={48} />
              <Tooltip labelFormatter={(v) => new Date(v).toLocaleString()} contentStyle={{ background: "var(--card)", border: "1px solid var(--border)" }} />
              <Area type="monotone" dataKey="clicks" name="Clicks" stroke="#4f46e5" fill="#4f46e5" fillOpacity={0.15} />
              <Area type="monotone" dataKey="unique_clicks" name="Unique" stroke="#10b981" fill="#10b981" fillOpacity={0.1} />
            </AreaChart>
          </ResponsiveContainer>
        ) : <div className="empty">No clicks in this range yet.</div>}
      </div>

      <div className="grid two">
        <div className="card">
          <h2>Campaign performance</h2>
          {camps.data && camps.data.length > 0 ? (
            <ResponsiveContainer width="100%" height={260}>
              <BarChart data={camps.data} layout="vertical" margin={{ left: 16 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" horizontal={false} />
                <XAxis type="number" stroke="var(--muted)" fontSize={12} />
                <YAxis type="category" dataKey="name" width={120} stroke="var(--muted)" fontSize={12} />
                <Tooltip contentStyle={{ background: "var(--card)", border: "1px solid var(--border)" }} />
                <Bar dataKey="clicks" name="Clicks" fill="#4f46e5" radius={[0, 4, 4, 0]} />
                <Bar dataKey="unique_clicks" name="Unique" fill="#10b981" radius={[0, 4, 4, 0]} />
              </BarChart>
            </ResponsiveContainer>
          ) : <div className="empty">No data.</div>}
        </div>

        <div className="card">
          <h2>Top-performing ads</h2>
          {ads.data && ads.data.length > 0 ? (
            <table>
              <thead><tr><th>Ad</th><th className="num">Clicks</th><th className="num">Unique</th><th className="num">CTR</th></tr></thead>
              <tbody>
                {ads.data.map((a) => (
                  <tr key={a.ad_id}>
                    <td>{a.name}<div className="sub">{a.ad_id}</div></td>
                    <td className="num">{fmt(a.clicks)}</td>
                    <td className="num">{fmt(a.unique_clicks)}</td>
                    <td className="num">{a.impressions > 0 ? pct(a.ctr) : "n/a"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : <div className="empty">No data.</div>}
        </div>

        <div className="card">
          <h2>Clicks by country</h2>
          {countries.data && countries.data.length > 0 ? (
            <ResponsiveContainer width="100%" height={260}>
              <BarChart data={countries.data.slice(0, 10)}>
                <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" vertical={false} />
                <XAxis dataKey="name" stroke="var(--muted)" fontSize={12} />
                <YAxis stroke="var(--muted)" fontSize={12} width={48} />
                <Tooltip contentStyle={{ background: "var(--card)", border: "1px solid var(--border)" }} />
                <Bar dataKey="clicks" name="Clicks" fill="#06b6d4" radius={[4, 4, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          ) : <div className="empty">No data.</div>}
        </div>

        <div className="card">
          <h2>Clicks by device</h2>
          {devices.data && devices.data.length > 0 ? (
            <ResponsiveContainer width="100%" height={260}>
              <PieChart>
                <Pie data={devices.data} dataKey="clicks" nameKey="name" innerRadius={55} outerRadius={90} label>
                  {devices.data.map((_, i) => <Cell key={i} fill={COLORS[i % COLORS.length]} />)}
                </Pie>
                <Tooltip contentStyle={{ background: "var(--card)", border: "1px solid var(--border)" }} />
              </PieChart>
            </ResponsiveContainer>
          ) : <div className="empty">No data.</div>}
        </div>
      </div>
    </div>
  );
}

function Kpi({ title, value, hint }: { title: string; value: string; hint: string }) {
  return (
    <div className="card kpi">
      <h2>{title}</h2>
      <div className="value">{value}</div>
      <div className="hint">{hint}</div>
    </div>
  );
}

function tickLabel(v: string, g: Granularity) {
  const d = new Date(v);
  return g === "day"
    ? d.toLocaleDateString([], { month: "short", day: "numeric" })
    : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

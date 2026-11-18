export type Summary = {
  total_clicks: number;
  unique_clicks: number;
  impressions: number;
  ctr: number;
  clicks_per_minute: number;
};
export type Point = { bucket: string; clicks: number; unique_clicks: number };
export type AdStat = {
  ad_id: string;
  name: string;
  campaign_id: string;
  clicks: number;
  unique_clicks: number;
  impressions: number;
  ctr: number;
};
export type CampaignStat = {
  campaign_id: string;
  name: string;
  advertiser_id: string;
  clicks: number;
  unique_clicks: number;
};
export type Slice = { name: string; clicks: number };

export type Range = { from: Date; to: Date };
export type Granularity = "minute" | "hour" | "day";

// Pick a bucket size that keeps charts readable.
export function autoGranularity(r: Range): Granularity {
  const hours = (r.to.getTime() - r.from.getTime()) / 3_600_000;
  if (hours <= 3) return "minute";
  if (hours <= 72) return "hour";
  return "day";
}

export async function getJSON<T>(path: string, params: Record<string, string>): Promise<T> {
  const qs = new URLSearchParams(params).toString();
  const res = await fetch(`/api/v1/${path}?${qs}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json();
}

export const rangeParams = (r: Range) => ({ from: r.from.toISOString(), to: r.to.toISOString() });

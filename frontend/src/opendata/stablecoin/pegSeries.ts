import { withGaps } from './charts.ts';
import type { StableDexPoint, StablePegPoint, StableRelative } from './types.ts';

const DATE_ONLY_RE = /^\d{4}-\d{2}-\d{2}$/;
const ISO_DATE_PREFIX_RE = /^\d{4}-\d{2}-\d{2}/;
const DAILY_GAP_MS = 2 * 86_400_000;

/**
 * Returns true when the relative peg chart plots daily buckets (series_resolution === '1d')
 * or when either series already contains YYYY-MM-DD date-only timestamps.
 */
export function isDailyResolution(seriesResolution: string, ...groups: { ts: string }[][]): boolean {
  if (seriesResolution === '1d') return true;
  return groups.some(pts => pts.some(p => DATE_ONLY_RE.test(p.ts)));
}

/** Normalizes ISO timestamps to YYYY-MM-DD when plotting daily buckets. */
export function normalizePegTimestamp(ts: string, daily: boolean): string {
  if (!daily || !ISO_DATE_PREFIX_RE.test(ts)) return ts;
  return ts.slice(0, 10);
}

/** Normalizes timestamps and collapses multiple daily readings to the latest point per UTC day. */
export function normalizeSeriesTimestamps<T extends { ts: string }>(points: T[], daily: boolean): T[] {
  if (!daily) return points;
  const byDay = new Map<string, T>();
  for (const p of points) {
    const day = normalizePegTimestamp(p.ts, true);
    byDay.set(day, p.ts === day ? p : { ...p, ts: day });
  }
  return [...byDay.values()];
}

function toPegChartData<T extends { ts: string; dev_bps: number | null }>(
  points: T[],
  daily: boolean,
  intradayGapMs: number,
): [string, number | null][] {
  const normalized = normalizeSeriesTimestamps(points, daily);
  return withGaps(
    normalized.map(p => [p.ts, p.dev_bps] as [string, number | null]),
    p => p[0],
    daily ? DAILY_GAP_MS : intradayGapMs,
    at => [at, null] as [string, number | null],
  );
}

/**
 * Builds gap-aware ECharts series pairs for the relative peg chart, keeping
 * exchange-median daily buckets (YYYY-MM-DD) and Uniswap points (RFC3339) on
 * identical UTC day ticks on daily ranges while preserving 5m/10m intraday ticks.
 */
export function buildRelativePegSeries(
  rel: Pick<StableRelative, 'series_resolution' | 'series' | 'uniswap_series'>,
): {
  daily: boolean;
  medianData: [string, number | null][];
  uniswapData: [string, number | null][];
} {
  const daily = isDailyResolution(rel.series_resolution, rel.series, rel.uniswap_series);
  return {
    daily,
    medianData: toPegChartData<StablePegPoint>(rel.series, daily, 15 * 60_000),
    uniswapData: toPegChartData<StableDexPoint>(rel.uniswap_series, daily, 30 * 60_000),
  };
}

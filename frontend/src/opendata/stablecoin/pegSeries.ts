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

function compareTimestamps(aTs: string, bTs: string): number {
  const tA = Date.parse(aTs);
  const tB = Date.parse(bTs);
  if (!Number.isNaN(tA) && !Number.isNaN(tB)) return tA - tB;
  if (aTs === bTs) return 0;
  return aTs < bTs ? -1 : 1;
}

/** Normalizes timestamps to ascending chronological order, keeping the latest reading per UTC day on daily series. */
export function normalizeSeriesTimestamps<T extends { ts: string }>(points: T[], daily: boolean): T[] {
  if (!daily) {
    return [...points].sort((a, b) => compareTimestamps(a.ts, b.ts));
  }
  const byDay = new Map<string, { point: T; timeMs: number; index: number }>();
  points.forEach((p, index) => {
    const parsed = Date.parse(p.ts);
    const day = Number.isNaN(parsed)
      ? normalizePegTimestamp(p.ts, true)
      : new Date(parsed).toISOString().slice(0, 10);
    const timeMs = Number.isNaN(parsed) ? Number.NEGATIVE_INFINITY : parsed;
    const prev = byDay.get(day);
    if (!prev || timeMs > prev.timeMs || (timeMs === prev.timeMs && index >= prev.index)) {
      byDay.set(day, {
        point: p.ts === day ? p : { ...p, ts: day },
        timeMs,
        index,
      });
    }
  });
  return [...byDay.entries()]
    .sort(([dayA, a], [dayB, b]) => compareTimestamps(dayA, dayB) || a.index - b.index)
    .map(([, entry]) => entry.point);
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

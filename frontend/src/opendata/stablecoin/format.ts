// Stablecoin formatters (stablecoin.md §7.3): units always visible; null,
// undefined and NaN render "—", never 0 (§12.1). Fixed en-US formatting and
// UTC times, so every reader sees the same numbers and the helpers are
// unit-tested. No imports: the Node test runner loads this file directly.

const DASH = '—';

const missing = (v: number | null | undefined): v is null | undefined =>
  v === null || v === undefined || Number.isNaN(v);

const grouped = (v: number, decimals: number): string =>
  v.toLocaleString('en-US', { minimumFractionDigits: decimals, maximumFractionDigits: decimals });

// compact abbreviates thousands of millions as B (and M, T); below 1,000 it
// keeps `small` decimals.
const compact = (abs: number, small: number): string => {
  if (abs >= 1e12) return `${(abs / 1e12).toFixed(2)}T`;
  if (abs >= 1e9) return `${(abs / 1e9).toFixed(2)}B`;
  if (abs >= 1e6) return `${(abs / 1e6).toFixed(2)}M`;
  if (abs >= 1e3) return grouped(abs, 0);
  return abs.toFixed(small);
};

// fmtBps: basis points with a sign (+ for positive when signed) and the unit.
export const fmtBps = (bps: number | null | undefined, decimals = 1, signed = true): string => {
  if (missing(bps)) return DASH;
  const sign = bps < 0 ? '-' : signed && bps > 0 ? '+' : '';
  return `${sign}${grouped(Math.abs(bps), decimals)} bps`;
};

const SYMBOL: Record<string, string> = { USD: '$', EUR: '€' };

// fmtPrice: a stablecoin price in its numéraire ($0.99987, €1.00012, or
// 1.00010 USDT for the relative peg).
export const fmtPrice = (price: number | null | undefined, numeraire: string, decimals = 5): string => {
  if (missing(price)) return DASH;
  const sym = SYMBOL[numeraire];
  return sym ? `${sym}${price.toFixed(decimals)}` : `${price.toFixed(decimals)} ${numeraire}`;
};

// fmtNative: an amount in token units (183.55B USDT).
export const fmtNative = (v: number | null | undefined, unit: string): string => {
  if (missing(v)) return DASH;
  return `${v < 0 ? '-' : ''}${compact(Math.abs(v), 2)} ${unit}`;
};

// fmtMoney: an amount in USD or EUR ($312.60B, €1.20B).
export const fmtMoney = (v: number | null | undefined, numeraire: string, decimals = 2): string => {
  if (missing(v)) return DASH;
  const sym = SYMBOL[numeraire] ?? '';
  const body = compact(Math.abs(v), decimals);
  return `${v < 0 ? '-' : ''}${sym}${body}${sym ? '' : ` ${numeraire}`}`;
};

const parse = (iso: string | null | undefined): Date | null => {
  if (!iso) return null;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? null : new Date(t);
};

const pad = (n: number): string => String(n).padStart(2, '0');

// fmtTimeHM: "13:20 UTC".
export const fmtTimeHM = (iso: string | null | undefined): string => {
  const d = parse(iso);
  return d ? `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())} UTC` : DASH;
};

// fmtDateTime: "2026-10-19 13:20 UTC".
export const fmtDateTime = (iso: string | null | undefined): string => {
  const d = parse(iso);
  if (!d) return DASH;
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())} UTC`;
};

// fmtShortAddr shortens a contract or account address for tables; the full
// value stays in the title and the explorer link.
export const fmtShortAddr = (addr: string | null | undefined): string => {
  if (!addr) return DASH;
  return addr.length <= 14 ? addr : `${addr.slice(0, 6)}…${addr.slice(-4)}`;
};

// fmtRangeWindow labels the ticker's 24h deviation range: "since HH:MM UTC"
// while the stored samples span less than 24 h (cold start, §7.4).
export const fmtRangeWindow = (from: string | null | undefined, partial: boolean): string =>
  partial && from ? `since ${fmtTimeHM(from)}` : '24h';

// fmtEurcBool renders an EURC boolean flag: absent values (null/undefined)
// render as "—", never "no".
export const fmtEurcBool = (v: boolean | null | undefined, falseDetail?: string, trueDetail?: string): string => {
  if (v === null || v === undefined) return DASH;
  if (v) return trueDetail ? `yes: ${trueDetail}` : 'yes';
  return falseDetail ? `no: ${falseDetail}` : 'no';
};

export interface EurMarketStats {
  sharePct: string;
  totalUsd: string;
  nativeTotal: string;
  nativeSub: string;
}

export const computeEurMarketStats = (
  rows: { core?: string; numeraire: string; circulating_native: number | null; circulating_usd: number | null }[],
): EurMarketStats => {
  const eur = rows.filter(r => r.numeraire === 'EUR');
  const eurUsdTotal = eur.reduce((s, r) => s + (r.circulating_usd ?? 0), 0);
  const eurWithNative = eur.filter(r => r.circulating_native != null);
  const allHaveNative = eur.length > 0 && eurWithNative.length === eur.length;
  const eurNativeTotal = allHaveNative ? eur.reduce((s, r) => s + (r.circulating_native ?? 0), 0) : null;
  const eurc = eur.find(r => r.core === 'eurc');
  return {
    sharePct: eurc?.circulating_usd != null && eurUsdTotal > 0 ? `${((eurc.circulating_usd / eurUsdTotal) * 100).toFixed(1)}%` : DASH,
    totalUsd: eurUsdTotal > 0 ? fmtMoney(eurUsdTotal, 'USD') : DASH,
    nativeTotal: eurNativeTotal !== null ? `€${fmtNative(eurNativeTotal, '').trim()}` : DASH,
    nativeSub: allHaveNative ? `${eur.length} coins` : `${eurWithNative.length} of ${eur.length} coins have native supply`,
  };
};

// fmtBytesGiB formats a byte count in GiB ("1.00 GiB") or "—" when missing.
export const fmtBytesGiB = (bytes: number | null | undefined, decimals = 2): string => {
  if (missing(bytes)) return DASH;
  return `${(bytes / 1024 ** 3).toFixed(decimals)} GiB`;
};

const fmtRelMinutes = (diffMin: number): string => {
  if (diffMin === 0) return 'now';
  const abs = Math.abs(diffMin);
  const body =
    abs < 60
      ? `${abs}m`
      : abs % 60 === 0
        ? `${Math.floor(abs / 60)}h`
        : `${Math.floor(abs / 60)}h ${abs % 60}m`;
  return diffMin > 0 ? `in ${body}` : `${body} ago`;
};

// fmtNextCheckUTC formats next_check_at in UTC alongside its relative time
// ("2026-10-19 14:00 UTC (in 39m)"), or "—" when null/missing.
export const fmtNextCheckUTC = (nextCheckIso: string | null | undefined, nowIso?: string | null): string => {
  const target = parse(nextCheckIso);
  if (!target) return DASH;
  const dt = fmtDateTime(nextCheckIso);
  const now = nowIso ? parse(nowIso) : new Date();
  if (!now) return dt;
  const diffMin = Math.round((target.getTime() - now.getTime()) / 60_000);
  return `${dt} (${fmtRelMinutes(diffMin)})`;
};

// fmtLastRunUTC formats a job's last run (UTC time, trigger, result, GiB billed)
// or "—" when the job has not run yet.
export const fmtLastRunUTC = (
  status:
    | {
        last_run_at?: string | null;
        last_run_trigger?: string | null;
        last_run_result?: string | null;
        last_run_bytes_billed?: number | null;
      }
    | null
    | undefined,
): string => {
  if (!status || !status.last_run_at) return DASH;
  const dt = fmtDateTime(status.last_run_at);
  if (dt === DASH) return DASH;
  const trigger = status.last_run_trigger || DASH;
  const result = status.last_run_result || DASH;
  const billed = fmtBytesGiB(status.last_run_bytes_billed);
  return `${dt} · ${trigger} · ${result} · ${billed}`;
};

// mapStableBQError maps HTTP 403, 409 and 429 from /bigquery/estimate and
// /bigquery/run to plain operator messages.
export const mapStableBQError = (status: number | undefined, serverMessage?: string): string => {
  const msg = (serverMessage ?? '').trim();
  if (status === 403) {
    return 'BigQuery actions are restricted to this computer (loopback 127.0.0.1 / ::1).';
  }
  if (status === 429) {
    return msg
      ? `Rate limited (at most one manual run every 5 minutes): ${msg}`
      : 'Rate limited: at most one manual run per job every 5 minutes.';
  }
  if (status === 409) {
    const lower = msg.toLowerCase();
    if (lower.includes('estimate changed')) {
      return 'The dry-run estimate changed by more than 10%. Click "Run now" again to review the updated estimate.';
    }
    if (lower.includes('already running')) {
      return 'This job is already running. Wait for the current run to finish.';
    }
    if (lower.includes('over_budget')) {
      return 'Refused: the estimated scan exceeds the remaining monthly BigQuery budget.';
    }
    if (lower.includes('nothing to do')) {
      return 'Already up to date through yesterday UTC.';
    }
    return msg || 'Conflict: this BigQuery job cannot be started right now.';
  }
  return msg || 'BigQuery request failed.';
};


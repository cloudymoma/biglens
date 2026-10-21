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

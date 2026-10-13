import type { CryptoPricesData } from '../../types';

// On-chain USD prices (GET /api/opendata/crypto/prices): daily ETH/USD and
// WBTC-derived BTC/USD from Uniswap v3 swaps. They are an optional overlay —
// native units stay primary and every helper degrades to "no USD" when a
// day's price is missing.

export const ONCHAIN_PRICE_CAPTION =
  'DEX-derived from Uniswap v3 pools (median of sampled hours) — not an exchange index';

export interface PriceLookup {
  eth: Map<string, number>; // YYYY-MM-DD → USD per ETH
  btc: Map<string, number>; // YYYY-MM-DD → USD per BTC (WBTC-derived)
}

export function priceLookup(data: CryptoPricesData | null): PriceLookup {
  const eth = new Map<string, number>();
  const btc = new Map<string, number>();
  for (const r of data?.rows ?? []) {
    if (r.eth_usd != null && Number.isFinite(r.eth_usd) && r.eth_usd > 0) eth.set(r.day, r.eth_usd);
    if (r.btc_usd != null && Number.isFinite(r.btc_usd) && r.btc_usd > 0) btc.set(r.day, r.btc_usd);
  }
  return { eth, btc };
}

export function fmtUSD(v: number): string {
  const a = Math.abs(v);
  if (a >= 1e9) return `$${(v / 1e9).toFixed(2)}B`;
  if (a >= 1e6) return `$${(v / 1e6).toFixed(2)}M`;
  if (a >= 1e3) return `$${(v / 1e3).toFixed(1)}K`;
  return `$${v.toFixed(2)}`;
}

// "≈ $1.23M" for a native amount at a day's price; '' when either is missing.
export function approxUSD(amount: number | null | undefined, price: number | undefined): string {
  if (amount == null || price == null || !Number.isFinite(amount) || !Number.isFinite(price)) return '';
  return `≈ ${fmtUSD(amount * price)}`;
}

const escapeHtml = (s: string): string =>
  s.replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] as string);

interface AxisTooltipParam {
  seriesName?: string;
  value?: unknown;
  axisValue?: unknown;
  marker?: unknown;
}

// ECharts axis-tooltip formatter for daily native-unit series: each line
// shows the native amount plus "≈ $" at that day's on-chain price when known,
// optionally followed by a total line (e.g. miner revenue = subsidy + fees).
// Series names and dates are our own constants/validated dates and are
// escaped anyway; values are numbers.
export function usdAxisTooltip(
  seriesUnits: Record<string, { unit: string; prices: Map<string, number> }>,
  fmtNative: (n: number) => string,
  total?: { label: string; unit: string; prices: Map<string, number> },
) {
  return (raw: unknown): string => {
    const params = (Array.isArray(raw) ? raw : [raw]) as AxisTooltipParam[];
    const day = String(params[0]?.axisValue ?? '');
    const lines = [escapeHtml(day)];
    let sum = 0;
    let summed = 0;
    for (const p of params) {
      const name = String(p.seriesName ?? '');
      const v = typeof p.value === 'number' ? p.value : NaN; // null = no data that day
      if (!Number.isFinite(v)) continue;
      sum += v;
      summed++;
      const meta = seriesUnits[name];
      const usd = meta ? approxUSD(v, meta.prices.get(day)) : '';
      const marker = typeof p.marker === 'string' ? p.marker : '';
      lines.push(`${marker}${escapeHtml(name)}: ${fmtNative(v)}${meta ? ` ${meta.unit}` : ''}${usd ? ` (${usd})` : ''}`);
    }
    if (total && summed > 1) {
      const usd = approxUSD(sum, total.prices.get(day));
      lines.push(`${escapeHtml(total.label)}: ${fmtNative(sum)} ${total.unit}${usd ? ` (${usd})` : ''}`);
    }
    return lines.join('<br/>');
  };
}

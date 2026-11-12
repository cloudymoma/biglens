import type { TokenSupplyRow } from '../../types';

export type LiveStableAsset = 'USDT' | 'USDC' | 'EURC';

export interface AssetSupplySummary {
  total: number | null;
  partial: boolean;
  knownRows: number;
  failedRows: number;
  failedNetworks: string[];
}

export type LiveSupplyTotals = Record<LiveStableAsset, AssetSupplySummary>;

const ASSETS: readonly LiveStableAsset[] = ['USDT', 'USDC', 'EURC'];

function emptySummary(): AssetSupplySummary {
  return {
    total: null,
    partial: false,
    knownRows: 0,
    failedRows: 0,
    failedNetworks: [],
  };
}

export const fmtCompactSupply = (n: number): string =>
  n >= 1e9 ? (n / 1e9).toFixed(2) + 'B'
  : n >= 1e6 ? (n / 1e6).toFixed(2) + 'M'
  : n >= 1e3 ? (n / 1e3).toFixed(1) + 'K'
  : String(Math.round(n * 100) / 100);

// summarizeLiveSupply sums non-errored finite supply rows per asset. Rows
// carrying an error or non-finite supply are excluded and mark that asset's
// total as partial so failed or malformed chain RPC data is never silently
// treated as zero or complete.
export function summarizeLiveSupply(rows: readonly TokenSupplyRow[]): LiveSupplyTotals {
  const out: LiveSupplyTotals = {
    USDT: emptySummary(),
    USDC: emptySummary(),
    EURC: emptySummary(),
  };

  for (const r of rows) {
    if (!(ASSETS as readonly string[]).includes(r.asset)) continue;
    const entry = out[r.asset as LiveStableAsset];
    if (r.error || !Number.isFinite(r.supply)) {
      entry.partial = true;
      entry.failedRows++;
      const netLabel = r.network_label || r.network;
      if (netLabel && !entry.failedNetworks.includes(netLabel)) {
        entry.failedNetworks.push(netLabel);
      }
      continue;
    }
    entry.total = (entry.total ?? 0) + r.supply;
    entry.knownRows++;
  }

  return out;
}

// barSupplyValue returns the summed native/bridged supply for one (asset, net,
// tier) bar, or null when no valid (non-errored) rows exist.
export function barSupplyValue(
  rows: readonly TokenSupplyRow[],
  asset: string,
  network: string,
  tier: string,
): number | null {
  let sum = 0;
  let validCount = 0;
  for (const r of rows) {
    if (r.asset !== asset || r.network !== network || r.tier !== tier) continue;
    if (r.error || !Number.isFinite(r.supply)) continue;
    sum += r.supply;
    validCount++;
  }
  return validCount > 0 ? sum : null;
}

export function formatSupplySummary(
  label: string,
  unit: string,
  summary: AssetSupplySummary,
  fmt: (n: number) => string = fmtCompactSupply,
): string {
  if (summary.total === null) {
    return `${label} —`;
  }
  return `${label} ${unit}${fmt(summary.total)}${summary.partial ? ' (partial)' : ''}`;
}

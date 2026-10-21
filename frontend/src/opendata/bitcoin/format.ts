const SATS_PER_BTC = 100_000_000;

export const satsToBTC = (sats: number): number => sats / SATS_PER_BTC;

export const fmtBTC = (btc: number | null | undefined, decimals = 2): string => {
  if (btc === null || btc === undefined || Number.isNaN(btc)) return '—';
  if (Math.abs(btc) >= 1_000_000) return `${(btc / 1_000_000).toFixed(2)}M BTC`;
  if (Math.abs(btc) >= 1_000) return `${btc.toLocaleString(undefined, { maximumFractionDigits: 0 })} BTC`;
  return `${btc.toLocaleString(undefined, { minimumFractionDigits: decimals, maximumFractionDigits: decimals })} BTC`;
};

export const fmtBTCFromSats = (sats: number | null | undefined, decimals = 2): string => {
  if (sats === null || sats === undefined || Number.isNaN(sats)) return '—';
  return fmtBTC(sats / SATS_PER_BTC, decimals);
};

export const fmtSats = (sats: number | null | undefined): string => {
  if (sats === null || sats === undefined || Number.isNaN(sats)) return '—';
  return `${Math.round(sats).toLocaleString()} sats`;
};

export const fmtSatVB = (rate: number | null | undefined): string => {
  if (rate === null || rate === undefined || Number.isNaN(rate)) return '—';
  if (rate < 10) return `${rate.toFixed(2)} sat/vB`;
  return `${rate.toFixed(1)} sat/vB`;
};

import { BTC_COLOR } from '../crypto/shared';

export interface RigSpec {
  name: string;
  jth: number;
  color: string;
}

// Representative ASIC generations, oldest to newest. Efficiency in J/TH.
export const RIGS: RigSpec[] = [
  { name: 'Antminer S9 (2016)', jth: 98, color: '#a1a1aa' },
  { name: 'Antminer S19 Pro (2020)', jth: 29.5, color: '#60a5fa' },
  { name: 'Antminer S19 XP (2022)', jth: 21.5, color: '#a78bfa' },
  { name: 'Antminer S21 (2023)', jth: 17.5, color: BTC_COLOR },
  { name: 'Antminer S21 XP (2024)', jth: 13.5, color: '#22c55e' },
];

// A sustained 1 TH/s burns e J/TH × 86400 s/day = 0.024·e kWh/day.
export const KWH_PER_JTH_DAY = 0.024;

// Operating assumptions beyond wall-plug wattage: PUE multiplies energy
// (cooling, fans, transformer losses; 1.05–1.2 typical), pool fee shaves
// revenue (1–2.5% typical).
export interface Assumptions {
  pue: number;
  poolFeePct: number;
}

// BTC earned per day by 1 TH/s given daily hashprice in BTC/PH/day (1 PH/s = 1,000 TH/s),
// net of the pool fee.
export function btcPerThDay(btcPerPHDay: number, poolFeePct: number): number {
  if (btcPerPHDay <= 0) return 0;
  const clampedFee = Math.min(99, Math.max(0, poolFeePct));
  return (btcPerPHDay / 1000) * (1 - clampedFee / 100);
}

// Shutdown price: the electricity price ($/kWh) at which net revenue exactly
// covers power. Above it a running rig loses money on every kWh.
export function breakEvenKwh(
  btcPerPHDay: number,
  jth: number,
  priceUsd: number,
  a: Assumptions,
): number {
  if (jth <= 0 || a.pue <= 0 || priceUsd <= 0) return 0;
  return (btcPerThDay(btcPerPHDay, a.poolFeePct) * priceUsd) / (KWH_PER_JTH_DAY * jth * a.pue);
}

// Electricity cost ($) to net one whole BTC with a rig of efficiency jth.
export function costPerBtc(
  btcPerPHDay: number,
  jth: number,
  elecKwhUsd: number,
  a: Assumptions,
): number {
  const perDay = btcPerThDay(btcPerPHDay, a.poolFeePct);
  if (perDay <= 0 || jth <= 0 || a.pue <= 0 || elecKwhUsd <= 0) return 0;
  return ((KWH_PER_JTH_DAY * jth * a.pue) / perDay) * elecKwhUsd;
}

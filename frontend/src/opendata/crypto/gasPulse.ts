import type { GasPulseChain } from '../../types';
import { CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE } from './shared';

export const GAS_CHAIN_COLORS: Record<string, string> = {
  btc: '#f7931a', eth: '#627eea', arb: '#28a0f0', op: '#ff0420',
  poly: '#8247e5', tron: '#ef0027', sol: '#14f195',
};

const compact = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 2 });

// fmtGas keeps sub-gwei L2 fees readable and compacts TRON's billions of energy.
export function fmtGas(v: number): string {
  const a = Math.abs(v);
  if (a >= 1e6) return compact.format(v);
  if (a >= 1) return v.toLocaleString('en', { maximumFractionDigits: 2 });
  if (a === 0) return '0';
  return v.toPrecision(3);
}

// fmtAllTime shows tiny base fees in wei (Optimism's floor is 50 wei).
export function fmtAllTime(v: number, unit: string): string {
  if (unit === 'gwei' && v < 1e-4) return `${Math.round(v * 1e9).toLocaleString('en')} wei`;
  return `${fmtGas(v)} ${unit}`;
}

// "2022-05-01T01:09:03Z" -> "2022-05-01 01:09 UTC"; '' -> "genesis".
export const fmtWhen = (t: string): string => (t ? `${t.slice(0, 16).replace('T', ' ')} UTC` : 'genesis');

// "2026-10-04 01:00" -> "10-04 01:00".
export const shortHour = (h: string): string => h.slice(5);

export type PercentileTone = 'low' | 'normal' | 'high';

// Bands from gas_fee_design.md §3.2: P0–35 quiet, P36–79 normal, P80+ congested.
export function percentileTone(p: number): PercentileTone {
  if (p <= 35) return 'low';
  if (p >= 80) return 'high';
  return 'normal';
}

export const TONE_COLOR: Record<PercentileTone, string> = {
  low: '#34d399', normal: '#fbbf24', high: '#f87171',
};

const dash = (v: number | null, unit: string): string => (v == null ? '—' : `${fmtGas(v)} ${unit}`);

// Two stacked grids sharing one hour axis: fee (band + primary line) on top,
// load bars below. The band is drawn as a transparent base plus a filled
// span; the tooltip reads raw hours so it shows low–high, not the span.
export function buildGasPulseOption(chain: GasPulseChain) {
  const { meta, hours } = chain;
  const color = GAS_CHAIN_COLORS[meta.id] ?? '#a1a1aa';
  const x = hours.map(h => shortHour(h.hour_utc));
  const hasBand = meta.band_label !== '';
  const bandSeries = hasBand
    ? [
        { name: 'band-base', type: 'line', data: hours.map(h => h.band_low), stack: 'band',
          symbol: 'none', lineStyle: { opacity: 0 } },
        { name: meta.band_label, type: 'line', stack: 'band', symbol: 'none', lineStyle: { opacity: 0 },
          areaStyle: { color, opacity: 0.15 },
          data: hours.map(h => (h.band_low == null || h.band_high == null ? null : h.band_high - h.band_low)) },
      ]
    : [];
  return {
    tooltip: {
      ...CHART_TOOLTIP,
      trigger: 'axis',
      formatter: (params: { dataIndex: number }[]) => {
        const h = hours[params[0].dataIndex];
        const lines = [`${h.hour_utc} UTC`, `${meta.primary_label}: ${dash(h.primary_val, meta.primary_unit)}`];
        if (hasBand && h.band_low != null && h.band_high != null) {
          lines.push(`${meta.band_label}: ${fmtGas(h.band_low)} – ${fmtGas(h.band_high)} ${meta.primary_unit}`);
        }
        lines.push(`${meta.load_label}: ${dash(h.load_val, meta.load_unit)}`);
        return lines.join('<br/>');
      },
    },
    axisPointer: { link: [{ xAxisIndex: 'all' }] },
    grid: [
      { left: 64, right: 24, top: 28, height: '52%' },
      { left: 64, right: 24, top: '72%', height: '18%' },
    ],
    xAxis: [
      { type: 'category', data: x, gridIndex: 0, axisLabel: { show: false } },
      { type: 'category', data: x, gridIndex: 1, axisLabel: AXIS_LABEL },
    ],
    yAxis: [
      { type: 'value', gridIndex: 0, scale: true, name: meta.primary_unit, nameTextStyle: AXIS_LABEL,
        axisLabel: { ...AXIS_LABEL, formatter: fmtGas }, splitLine: SPLIT_LINE },
      { type: 'value', gridIndex: 1, name: meta.load_unit, nameTextStyle: AXIS_LABEL,
        axisLabel: { ...AXIS_LABEL, formatter: fmtGas }, splitLine: SPLIT_LINE },
    ],
    series: [
      ...bandSeries,
      { name: meta.primary_label, type: 'line', data: hours.map(h => h.primary_val), symbol: 'none',
        connectNulls: false, lineStyle: { color, width: 2 }, itemStyle: { color } },
      { name: meta.load_label, type: 'bar', xAxisIndex: 1, yAxisIndex: 1,
        data: hours.map(h => h.load_val), itemStyle: { color, opacity: 0.45 } },
    ],
  };
}

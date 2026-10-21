// ECharts helpers for the stablecoin dashboard: theme-aware chrome, ARIA
// descriptions on every chart (§7.3) and tooltip HTML built only through
// tipHtml, which escapes every upstream string (venue names, pairs, chain
// labels, annotation titles) and accepts only #hex colors (§11, §12.4).
// Imports carry the .ts extension so the Node test runner can load this file.
import { escapeHtml } from '../common/format.ts';
import type { Theme } from './signals.ts';

export const ASSET_COLOR: Record<string, string> = {
  usdt: '#26A17B',
  usdc: '#2775CA',
  eurc: '#B45AA0',
  usdc_usdt: '#7C6CF2',
  uniswap: '#E8590C',
};

export interface Chrome {
  text: string;
  muted: string;
  axis: string;
  split: string;
  tooltipBg: string;
  tooltipBorder: string;
  tooltipText: string;
  band30: string; // ±30 bps band fill (S1 watch)
  line100: string; // ±100 bps lines (S1 warning)
  heat: string[]; // |deviation| bins, low → high
}

export function chrome(theme: Theme): Chrome {
  if (theme === 'light') {
    return {
      text: '#3f3f46',
      muted: '#71717a',
      axis: '#d4d4d8',
      split: '#e4e4e7',
      tooltipBg: '#ffffff',
      tooltipBorder: '#d4d4d8',
      tooltipText: '#18181b',
      band30: 'rgba(184,134,11,0.12)',
      line100: '#A63A00',
      // viridis: quiet cells pale, large deviations dark
      heat: ['#FDE725', '#90D743', '#35B779', '#21918C', '#31688E', '#440154'],
    };
  }
  return {
    text: '#d4d4d8',
    muted: '#a1a1aa',
    axis: '#3f3f46',
    split: '#1f1f23',
    tooltipBg: 'rgba(17,17,20,0.96)',
    tooltipBorder: '#3f3f46',
    tooltipText: '#e4e4e7',
    band30: 'rgba(234,179,8,0.12)',
    line100: '#D9480F',
    // viridis reversed: quiet cells dark, large deviations bright
    heat: ['#440154', '#31688E', '#21918C', '#35B779', '#90D743', '#FDE725'],
  };
}

// Heatmap bins on |deviation| in bps, aligned with S1 (30, 100) and S2 (25).
export const HEAT_BINS: { min: number; max?: number; label: string }[] = [
  { min: 0, max: 2, label: '< 2 bps' },
  { min: 2, max: 5, label: '2–5' },
  { min: 5, max: 10, label: '5–10' },
  { min: 10, max: 30, label: '10–30' },
  { min: 30, max: 100, label: '30–100' },
  { min: 100, label: '≥ 100 bps' },
];

const SAFE_COLOR = /^#[0-9a-fA-F]{3,8}$/;

export interface TipRow {
  label: string;
  value: string;
  color?: string;
}

// tipHtml is the only way tooltip HTML is built: header, labels and values
// are escaped; a color is used only when it is a #hex literal.
export function tipHtml(header: string, rows: TipRow[], note?: string): string {
  const head = `<div style="font-weight:600;margin-bottom:4px">${escapeHtml(header)}</div>`;
  const body = rows
    .map(r => {
      const dot =
        r.color && SAFE_COLOR.test(r.color)
          ? `<span style="display:inline-block;width:8px;height:8px;border-radius:50%;margin-right:6px;background:${r.color}"></span>`
          : '';
      return `<div style="display:flex;justify-content:space-between;gap:16px"><span>${dot}${escapeHtml(r.label)}</span><span style="font-family:monospace">${escapeHtml(r.value)}</span></div>`;
    })
    .join('');
  const foot = note ? `<div style="margin-top:4px;opacity:0.75">${escapeHtml(note)}</div>` : '';
  return head + body + foot;
}

// baseOption: shared chrome plus the chart's ARIA description. Time axes
// use UTC, like every timestamp in the dashboard.
export function baseOption(theme: Theme, description: string) {
  const c = chrome(theme);
  return {
    aria: { enabled: true, label: { description } },
    useUTC: true,
    backgroundColor: 'transparent',
    animation: false,
    textStyle: { color: c.text, fontFamily: 'Inter, system-ui, sans-serif' },
    tooltip: {
      backgroundColor: c.tooltipBg,
      borderColor: c.tooltipBorder,
      textStyle: { color: c.tooltipText, fontSize: 12 },
      confine: true,
    },
    legend: { textStyle: { color: c.text, fontSize: 11 }, top: 0 },
  };
}

export function axisStyle(theme: Theme) {
  const c = chrome(theme);
  return {
    axisLine: { lineStyle: { color: c.axis } },
    axisLabel: { color: c.muted, fontSize: 10 },
    splitLine: { lineStyle: { color: c.split, type: 'dashed' as const } },
  };
}

// colorOf returns a series color from ECharts tooltip params only when it is
// a plain #hex string (gradients and objects are dropped).
export function colorOf(v: unknown): string | undefined {
  return typeof v === 'string' && SAFE_COLOR.test(v) ? v : undefined;
}

// withGaps inserts a gap point (made by gap, usually a null value at the
// timestamp before the gap) wherever consecutive points are more than
// maxGapMs apart, so lines break across missing samples instead of drawing a
// straight line through them.
export function withGaps<T>(points: T[], ts: (p: T) => string, maxGapMs: number, gap: (at: string) => T): T[] {
  const out: T[] = [];
  let prev = Number.NaN;
  let prevTs = '';
  for (const p of points) {
    const t = Date.parse(ts(p));
    if (!Number.isNaN(prev) && !Number.isNaN(t) && t - prev > maxGapMs) out.push(gap(prevTs));
    out.push(p);
    prev = t;
    prevTs = ts(p);
  }
  return out;
}

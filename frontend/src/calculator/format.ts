export const CHART_TOOLTIP = {
  backgroundColor: 'rgba(17,17,20,0.95)',
  borderColor: '#27272a',
  textStyle: { color: '#e4e4e7', fontSize: 12 },
} as const;

export const AXIS_LABEL = { color: '#71717a', fontSize: 10 } as const;
export const SPLIT_LINE = { lineStyle: { color: '#27272a' } } as const;

// Fixed categorical order; series keep their colour regardless of count.
export const SERIES_COLORS = ['#38bdf8', '#fbbf24', '#c084fc', '#4ade80', '#fb7185', '#a3e635'] as const;

export function fmtUSD(n: number, digits = 2): string {
  return n.toLocaleString('en-US', {
    style: 'currency', currency: 'USD', minimumFractionDigits: digits, maximumFractionDigits: digits,
  });
}

export function fmtNum(n: number, digits = 0): string {
  return n.toLocaleString('en-US', { maximumFractionDigits: digits });
}

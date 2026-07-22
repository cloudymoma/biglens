import React from 'react';

// Brand-standard chain colors used across all four tabs.
export const BTC_COLOR = '#f7931a';
export const ETH_COLOR = '#627eea';

// Chart chrome mirrors GdeltDashboard so Open Data dashboards read as one system.
export const CHART_TOOLTIP = {
  backgroundColor: 'rgba(17,17,20,0.95)',
  borderColor: '#27272a',
  textStyle: { color: '#e4e4e7', fontSize: 12 },
};

export const AXIS_LABEL = { color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' };
export const SPLIT_LINE = { lineStyle: { color: '#1f1f23', type: 'dashed' } };

export const fmtNum = (n: number): string =>
  n >= 1e9 ? (n / 1e9).toFixed(2) + 'B'
  : n >= 1e6 ? (n / 1e6).toFixed(2) + 'M'
  : n >= 1e3 ? (n / 1e3).toFixed(1) + 'K'
  : String(Math.round(n * 100) / 100);

export const shortHash = (h: string): string => (h.length > 16 ? `${h.slice(0, 8)}…${h.slice(-6)}` : h);

export function Panel({ title, note, children }: {
  title: string; note?: string; children: React.ReactNode;
}) {
  return (
    <div className="rounded-2xl border border-zinc-800/50 p-5" style={{ background: '#111114' }}>
      <div className="flex items-center justify-between mb-4 gap-4">
        <h3 className="text-sm font-semibold text-white">{title}</h3>
        {note && <span className="text-[11px] text-zinc-600 text-right">{note}</span>}
      </div>
      {children}
    </div>
  );
}

export function DaysPicker({ options, value, onChange }: {
  options: number[]; value: number; onChange: (d: number) => void;
}) {
  return (
    <div className="flex items-center gap-1">
      {options.map(d => (
        <button
          key={d}
          onClick={() => onChange(d)}
          className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors ${
            value === d ? 'bg-zinc-700 text-white' : 'text-zinc-500 hover:text-zinc-300'
          }`}
        >
          {d === 365 ? '1 year' : `${d} days`}
        </button>
      ))}
    </div>
  );
}

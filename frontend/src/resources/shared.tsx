/* eslint-disable react-refresh/only-export-components */
import type React from 'react';

export const CHART_TOOLTIP = {
  backgroundColor: '#18181b',
  borderColor: '#3f3f46',
  textStyle: { color: '#e4e4e7', fontSize: 11 },
} as const;

export const AXIS_LABEL = { color: '#71717a', fontSize: 10 } as const;

export const th = 'text-left text-[11px] uppercase tracking-wide text-zinc-500 font-medium py-1.5 pr-4';
export const td = 'py-1.5 pr-4 text-zinc-300';

export interface ResTabProps {
  project: string;
  refreshKey: number;
}

export function fmtBytes(n: number): string {
  if (n >= 1e12) return `${(n / 1e12).toFixed(2)} TB`;
  if (n >= 1e9) return `${(n / 1e9).toFixed(2)} GB`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(2)} MB`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)} KB`;
  return `${n} B`;
}

export function Panel({ title, note, children }: { title: string; note?: string; children: React.ReactNode }) {
  return (
    <div className="bg-zinc-900/60 border border-zinc-800/60 rounded-xl p-4">
      <div className="flex items-baseline justify-between mb-3">
        <h3 className="text-sm font-medium text-zinc-200">{title}</h3>
        {note && <span className="text-[10px] text-zinc-500">{note}</span>}
      </div>
      {children}
    </div>
  );
}

export function WorkloadBadge({ workload }: { workload: string }) {
  const color =
    workload === 'GKE' ? 'text-sky-300 border-sky-800/60 bg-sky-950/40'
    : workload === 'Dataproc' ? 'text-emerald-300 border-emerald-800/60 bg-emerald-950/40'
    : workload === 'Composer' ? 'text-violet-300 border-violet-800/60 bg-violet-950/40'
    : 'text-zinc-400 border-zinc-700/60 bg-zinc-900/40';
  return <span className={`inline-block text-[10px] px-1.5 py-0.5 rounded border ${color}`}>{workload}</span>;
}

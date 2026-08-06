import type React from 'react';

export const NET_COLOR = '#34d399';
export const GROSS_COLOR = '#60a5fa';

export const CHART_TOOLTIP = {
  backgroundColor: '#18181b',
  borderColor: '#3f3f46',
  textStyle: { color: '#e4e4e7', fontSize: 11 },
} as const;

export const AXIS_LABEL = { color: '#71717a', fontSize: 10 } as const;

// Account-currency money formatting; billing exports carry an ISO code.
export function fmtMoney(n: number, currency: string): string {
  const abs = Math.abs(n);
  const compact =
    abs >= 1e9 ? `${(n / 1e9).toFixed(2)}B`
    : abs >= 1e6 ? `${(n / 1e6).toFixed(2)}M`
    : abs >= 1e3 ? `${(n / 1e3).toFixed(1)}K`
    : n.toFixed(2);
  return `${compact} ${currency || ''}`.trim();
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

export function MissingTableBanner({ table, dataset, docsUrl }: { table: string; dataset: string; docsUrl: string }) {
  return (
    <div className="border border-amber-800/50 bg-amber-950/30 rounded-xl p-4 text-sm text-amber-200">
      Export table <code className="text-amber-100">{table}</code> was not detected in{' '}
      <code className="text-amber-100">{dataset}</code>. Enable it in Cloud Billing to use this tab.{' '}
      <a href={docsUrl} target="_blank" rel="noreferrer" className="underline text-amber-100">
        Setup guide
      </a>
    </div>
  );
}

import type React from 'react';
import { ExternalLink } from 'lucide-react';

// Shared chrome for the GDELT tabs. Span caps mirror the backend limits.
export const MAX_EVENTS_DAYS = 90;
export const MAX_GKG_DAYS = 30;
export const MAX_STORIES_DAYS = 14;

export const CHART_TOOLTIP = {
  backgroundColor: 'rgba(17,17,20,0.95)',
  borderColor: '#27272a',
  textStyle: { color: '#e4e4e7', fontSize: 12 },
};

export const AXIS_LABEL = { color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' };
export const SPLIT_LINE = { lineStyle: { color: '#1f1f23', type: 'dashed' } };

// CAMEO event root codes ('01'-'20'); codes >= '10' are the conflict side.
export const CAMEO_LABELS: Record<string, string> = {
  '01': 'Public Statement', '02': 'Appeal', '03': 'Express Intent to Cooperate',
  '04': 'Consult', '05': 'Diplomatic Cooperation', '06': 'Material Cooperation',
  '07': 'Provide Aid', '08': 'Yield', '09': 'Investigate', '10': 'Demand',
  '11': 'Disapprove', '12': 'Reject', '13': 'Threaten', '14': 'Protest',
  '15': 'Exhibit Force Posture', '16': 'Reduce Relations', '17': 'Coerce',
  '18': 'Assault', '19': 'Fight', '20': 'Mass Violence',
};

export const cameoLabel = (code: string) => CAMEO_LABELS[code] || `Code ${code}`;

// Tone scale: <= -2 alarming, -2..2 neutral, >= 2 positive.
export const toneColor = (t: number) => (t <= -2 ? '#ef4444' : t < 2 ? '#fbbf24' : '#22c55e');

// Goldstein scale: negative = destabilizing, positive = cooperative.
export const goldsteinColor = (g: number) => (g <= -2 ? '#ef4444' : g < 2 ? '#fbbf24' : '#22c55e');

// GDELT dates are UTC (_PARTITIONDATE); all range math sticks to UTC.
export const fmtDate = (d: Date) => d.toISOString().slice(0, 10);
export const daysAgoUTC = (n: number) => fmtDate(new Date(Date.now() - n * 86400000));

export const spanOf = (start: string, end: string) =>
  Math.round((Date.parse(end) - Date.parse(start)) / 86400000) + 1;

// SOURCEURL is untrusted external data: only http/https URLs become links.
export function safeUrl(raw: string): URL | null {
  try {
    const u = new URL(raw);
    return u.protocol === 'http:' || u.protocol === 'https:' ? u : null;
  } catch {
    return null;
  }
}

// Compact article link showing only the hostname; falls back to plain text
// for non-http(s) identifiers (GKG can carry citations, not just URLs).
export function SourceLink({ raw }: { raw: string }) {
  const url = safeUrl(raw);
  if (!url) return <span className="text-zinc-600 truncate block" title={raw}>{raw}</span>;
  return (
    <a
      href={url.href}
      target="_blank"
      rel="noopener noreferrer"
      className="flex items-center gap-1 text-cyan-500 hover:text-cyan-300 truncate transition-colors"
      title={url.href}
    >
      <span className="truncate">{url.hostname}</span>
      <ExternalLink size={10} className="shrink-0" />
    </a>
  );
}

export function Section({ title, note, children }: {
  title: string; note?: string; children: React.ReactNode;
}) {
  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <h3 className="text-sm font-semibold text-white mb-1">{title}</h3>
      {note && <p className="text-xs text-zinc-500 mb-4">{note}</p>}
      {children}
    </div>
  );
}

export function DateInput({ label, value, min, max, onChange }: {
  label: string;
  value: string;
  min?: string;
  max?: string;
  onChange: (v: string) => void;
}) {
  return (
    <div>
      <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">{label}</label>
      <input
        type="date"
        value={value}
        min={min}
        max={max}
        onChange={e => e.target.value && onChange(e.target.value)}
        className="text-xs text-zinc-400 rounded-lg px-3 py-2 outline-none border border-zinc-800/50 transition-colors focus:border-cyan-500/30 [color-scheme:dark]"
        style={{ background: '#09090b' }}
      />
    </div>
  );
}

export function LoadingPulse() {
  return (
    <div className="space-y-4">
      {[1, 2, 3].map(i => (
        <div key={i} className="h-32 rounded-2xl animate-pulse" style={{ background: '#111114' }} />
      ))}
    </div>
  );
}

// Shown by tabs whose backend cap is tighter than the shell's 90-day range.
export function RangeTooWide({ maxDays, what }: { maxDays: number; what: string }) {
  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <p className="text-xs text-zinc-500 text-center py-8">
        {what} supports up to {maxDays} days — narrow the range to load this tab
      </p>
    </div>
  );
}

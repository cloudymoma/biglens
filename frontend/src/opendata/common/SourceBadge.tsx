import type { SourceStatus } from './types';
import { fmtRelativeAgo } from './format';

const DARK_STYLES: Record<string, string> = {
  ok: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300',
  stale: 'border-amber-500/30 bg-amber-500/10 text-amber-300',
  error: 'border-red-500/30 bg-red-500/10 text-red-300',
  disabled: 'border-zinc-700 bg-zinc-800/60 text-zinc-400',
  geo_blocked: 'border-amber-500/30 bg-amber-500/10 text-amber-300',
  rate_limited: 'border-amber-500/30 bg-amber-500/10 text-amber-300',
  degraded: 'border-amber-500/30 bg-amber-500/10 text-amber-300',
};

// Light-theme equivalents (the stablecoin dashboard has a light theme; the
// default stays dark, so the Bitcoin dashboard is unchanged).
const LIGHT_STYLES: Record<string, string> = {
  ok: 'border-emerald-600/40 bg-emerald-50 text-emerald-800',
  stale: 'border-amber-600/40 bg-amber-50 text-amber-800',
  error: 'border-red-600/40 bg-red-50 text-red-800',
  disabled: 'border-zinc-300 bg-zinc-100 text-zinc-600',
  geo_blocked: 'border-amber-600/40 bg-amber-50 text-amber-800',
  rate_limited: 'border-amber-600/40 bg-amber-50 text-amber-800',
  degraded: 'border-amber-600/40 bg-amber-50 text-amber-800',
};

export default function SourceBadge({
  source,
  weekly,
  theme = 'dark',
}: {
  source: SourceStatus;
  weekly?: boolean;
  theme?: 'light' | 'dark';
}) {
  const statusStyles = theme === 'light' ? LIGHT_STYLES : DARK_STYLES;
  const fallback = theme === 'light' ? 'border-zinc-300 bg-zinc-100 text-zinc-700' : 'border-zinc-700 bg-zinc-800/60 text-zinc-300';

  const cls = statusStyles[source.status] ?? fallback;
  const age = source.as_of ? fmtRelativeAgo(source.as_of) : '';
  const titleParts = [
    source.label,
    source.status,
    source.via ? `via ${source.via}` : '',
    age ? `as of ${age}` : '',
    source.license ? `License: ${source.license}` : '',
    source.error ? `Error: ${source.error}` : '',
  ].filter(Boolean);

  return (
    <span
      title={titleParts.join(' · ')}
      className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[10px] font-medium ${cls}`}
    >
      <span>{source.label}</span>
      {source.via && <span className="italic opacity-80">via {source.via}</span>}
      {weekly && <span className="opacity-80">· weekly</span>}
      {source.status === 'stale' && <span className="opacity-90">· stale</span>}
      {source.status === 'error' && <span className="opacity-90">· error</span>}
      {source.status === 'degraded' && <span className="opacity-90">· degraded</span>}
      {source.status === 'pending' && <span className="opacity-80">· pending</span>}
      {source.status === 'disabled' && <span className="opacity-80">· off</span>}
      {age && source.status === 'ok' && <span className="opacity-70">· {age}</span>}
    </span>
  );
}

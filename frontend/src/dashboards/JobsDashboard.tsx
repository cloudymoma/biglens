import { useState, useEffect, useMemo } from 'react';
import { Search, X, ListChecks } from 'lucide-react';
import type { QueryFilters, JobsDashboardData, JobRow } from '../types';
import { fetchJobsDashboard } from '../api';
import { formatBytes, EmptyState, ErrorBanner } from './shared';
import { ON_DEMAND_PER_TIB, TIB } from './pricing';

const INSIGHT_LABELS: { key: keyof JobRow; label: string }[] = [
  { key: 'slot_contention', label: 'Slot Contention' },
  { key: 'shuffle_quota', label: 'Shuffle Quota' },
  { key: 'high_card_join', label: 'High-Cardinality Join' },
  { key: 'partition_skew', label: 'Partition Skew' },
];

function fmtMs(ms: number): string {
  if (ms >= 60_000) return `${(ms / 60_000).toFixed(1)}m`;
  if (ms >= 1_000) return `${(ms / 1_000).toFixed(1)}s`;
  return `${Math.round(ms)}ms`;
}

export default function JobsDashboard({ filters }: { filters: QueryFilters }) {
  const [data, setData] = useState<JobsDashboardData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState('');
  const [selected, setSelected] = useState<JobRow | null>(null);

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError('');
    setSelected(null);
    fetchJobsDashboard(filters)
      .then(d => { if (active) setData(d); })
      .catch(e => { if (active) setError(e.response?.data || e.message); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [filters]);

  const jobs = useMemo(() => {
    const all = data?.jobs || [];
    if (!search) return all;
    const q = search.toLowerCase();
    return all.filter(j =>
      j.job_id.toLowerCase().includes(q) ||
      j.user_email.toLowerCase().includes(q) ||
      j.query.toLowerCase().includes(q));
  }, [data, search]);

  if (loading) return <LoadingPulse />;
  if (error) return <ErrorBanner message={error} />;
  if (!data) return null;

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-4">
        <div className="relative flex-1 max-w-md">
          <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-zinc-600" />
          <input
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search job id, user, or query text..."
            className="w-full text-xs text-zinc-300 rounded-lg pl-9 pr-3 py-2.5 outline-none border border-zinc-800/50 focus:border-cyan-500/30 placeholder:text-zinc-700"
            style={{ background: '#111114' }}
          />
        </div>
        <p className="text-xs text-zinc-600 font-mono shrink-0">
          {jobs.length} of {data.jobs?.length || 0} jobs (newest 100 in window)
        </p>
      </div>

      <div className="rounded-2xl border border-zinc-800/50 overflow-hidden" style={{ background: '#111114' }}>
        {jobs.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-zinc-500 border-b border-zinc-800/50">
                  <th className="text-left py-2.5 px-3 font-medium">Job ID</th>
                  <th className="text-left py-2.5 px-3 font-medium">User</th>
                  <th className="text-left py-2.5 px-3 font-medium">Type</th>
                  <th className="text-left py-2.5 px-3 font-medium">Status</th>
                  <th className="text-left py-2.5 px-3 font-medium">Created</th>
                  <th className="text-right py-2.5 px-3 font-medium">Queue</th>
                  <th className="text-right py-2.5 px-3 font-medium">Duration</th>
                  <th className="text-right py-2.5 px-3 font-medium">Slot Time</th>
                  <th className="text-right py-2.5 px-3 font-medium">Billed</th>
                  <th className="text-right py-2.5 px-3 font-medium">Est. $</th>
                  <th className="text-left py-2.5 px-3 font-medium">Cache</th>
                </tr>
              </thead>
              <tbody>
                {jobs.map((j, i) => (
                  <tr
                    key={i}
                    onClick={() => setSelected(j)}
                    className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors cursor-pointer"
                  >
                    <td className="py-2.5 px-3 text-white font-mono max-w-[180px] truncate" title={j.job_id}>{j.job_id}</td>
                    <td className="py-2.5 px-3 text-zinc-400 font-mono max-w-[150px] truncate" title={j.user_email}>{j.user_email}</td>
                    <td className="py-2.5 px-3 text-zinc-400 font-mono">{j.job_type}{j.statement_type ? `/${j.statement_type}` : ''}</td>
                    <td className="py-2.5 px-3">
                      {j.error_reason ? (
                        <span className="inline-flex px-2 py-0.5 rounded-full text-[10px] font-medium border text-rose-400 bg-rose-500/5 border-rose-500/15" title={j.error_reason}>
                          {j.error_reason}
                        </span>
                      ) : (
                        <span className="inline-flex px-2 py-0.5 rounded-full text-[10px] font-medium border text-emerald-400 bg-emerald-500/5 border-emerald-500/15">
                          {j.state}
                        </span>
                      )}
                    </td>
                    <td className="py-2.5 px-3 text-zinc-500 font-mono whitespace-nowrap">{j.creation_time.replace('T', ' ').replace('Z', '')}</td>
                    <td className="py-2.5 px-3 text-right text-zinc-400 font-mono">{fmtMs(j.queue_ms)}</td>
                    <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">{fmtMs(j.duration_ms)}</td>
                    <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">{(j.slot_ms / 1000).toFixed(1)}s</td>
                    <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">{formatBytes(j.bytes_billed)}</td>
                    <td className="py-2.5 px-3 text-right text-amber-400 font-mono">${((j.bytes_billed / TIB) * ON_DEMAND_PER_TIB).toFixed(4)}</td>
                    <td className="py-2.5 px-3 text-zinc-400 font-mono">{j.cache_hit ? 'HIT' : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState text="No jobs match the current filters" />
        )}
      </div>

      {/* Detail drawer */}
      {selected && (
        <div className="fixed inset-y-0 right-0 z-50 w-[520px] border-l border-zinc-800/60 shadow-2xl overflow-y-auto" style={{ background: '#0c0c0f' }}>
          <div className="sticky top-0 flex items-center justify-between px-5 py-4 border-b border-zinc-800/60" style={{ background: '#0c0c0f' }}>
            <div className="flex items-center gap-2 min-w-0">
              <ListChecks size={15} className="text-cyan-400 shrink-0" />
              <h3 className="text-sm font-semibold text-white font-mono truncate" title={selected.job_id}>{selected.job_id}</h3>
            </div>
            <button onClick={() => setSelected(null)} className="p-1.5 rounded-lg text-zinc-500 hover:text-zinc-200 hover:bg-zinc-800/40 cursor-pointer">
              <X size={16} />
            </button>
          </div>

          <div className="p-5 space-y-5 text-xs">
            <div className="grid grid-cols-2 gap-3">
              {[
                ['User', selected.user_email],
                ['Type', `${selected.job_type}${selected.statement_type ? ` / ${selected.statement_type}` : ''}`],
                ['State', selected.state],
                ['Created', selected.creation_time],
                ['Queue Time', fmtMs(selected.queue_ms)],
                ['Duration', fmtMs(selected.duration_ms)],
                ['Slot Time', `${(selected.slot_ms / 1000).toFixed(1)}s`],
                ['Bytes Billed', formatBytes(selected.bytes_billed)],
                ['Cache Hit', selected.cache_hit ? 'Yes' : 'No'],
                ['Reservation', selected.reservation || 'on-demand'],
              ].map(([k, v]) => (
                <div key={k} className="p-2.5 rounded-lg border border-zinc-800/30" style={{ background: '#111114' }}>
                  <p className="text-[9px] text-zinc-600 uppercase font-semibold tracking-wider mb-0.5">{k}</p>
                  <p className="text-zinc-300 font-mono break-all">{v}</p>
                </div>
              ))}
            </div>

            {selected.error_reason && (
              <div className="p-3 rounded-lg border border-rose-500/20 bg-rose-500/5">
                <p className="text-[9px] text-rose-400 uppercase font-semibold tracking-wider mb-1">Error</p>
                <p className="text-rose-300 font-mono">{selected.error_reason}</p>
              </div>
            )}

            {INSIGHT_LABELS.some(f => selected[f.key] === true) && (
              <div>
                <p className="text-[9px] text-zinc-600 uppercase font-semibold tracking-wider mb-2">Performance Insights</p>
                <div className="flex flex-wrap gap-1.5">
                  {INSIGHT_LABELS.filter(f => selected[f.key] === true).map(f => (
                    <span key={String(f.key)} className="px-2 py-1 rounded-md text-[10px] text-amber-400 bg-amber-500/10 border border-amber-500/15">{f.label}</span>
                  ))}
                </div>
              </div>
            )}

            {(selected.ref_tables?.length || 0) > 0 && (
              <div>
                <p className="text-[9px] text-zinc-600 uppercase font-semibold tracking-wider mb-2">Referenced Tables</p>
                <div className="space-y-1">
                  {selected.ref_tables!.map((t, i) => (
                    <p key={i} className="text-zinc-400 font-mono px-2.5 py-1.5 rounded-md border border-zinc-800/30" style={{ background: '#111114' }}>{t}</p>
                  ))}
                </div>
              </div>
            )}

            {selected.query && (
              <div>
                <p className="text-[9px] text-zinc-600 uppercase font-semibold tracking-wider mb-2">Query (first 1 KB)</p>
                <pre className="text-[11px] text-zinc-300 font-mono whitespace-pre-wrap break-all p-3 rounded-lg border border-zinc-800/30 max-h-[300px] overflow-y-auto" style={{ background: '#111114' }}>
                  {selected.query}
                </pre>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function LoadingPulse() {
  return (
    <div className="space-y-4">
      {[1, 2].map(i => (
        <div key={i} className="h-40 rounded-2xl animate-pulse" style={{ background: '#111114' }} />
      ))}
    </div>
  );
}

import { useState, useEffect } from 'react';
import { Activity } from 'lucide-react';
import type { SemPulseData, SemPulseRow } from '../../types';
import { fetchSemPulse } from '../../api';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';

// W5 — US Real-Time Pulse (US market only). The hourly table refreshes ~4×/day
// while the daily tables lag 1–2 days. Consecutive snapshots carry disjoint
// top-25 sets, so the acceleration signal is week-over-week within the
// snapshot's own weekly history, not Δrank between snapshots.

interface PulsePanelProps {
  onSelectTerm: (term: string) => void;
}

export default function PulsePanel({ onSelectTerm }: PulsePanelProps) {
  const [data, setData] = useState<SemPulseData | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    fetchSemPulse()
      .then(setData)
      .catch(e => setError(e.response?.data || e.message));
  }, []);

  if (error) return <ErrorBanner message={error} />;

  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <div className="flex items-center justify-between flex-wrap gap-2 mb-1">
        <h3 className="text-sm font-semibold text-white flex items-center gap-2">
          <Activity size={14} className="text-rose-400" /> US Real-Time Pulse
        </h3>
        {data && data.snapshot_time && (
          <span className="text-[10px] font-mono text-zinc-600">
            snapshot {data.snapshot_time.replace('T', ' ')} UTC · {snapshotAge(data.snapshot_time)}
          </span>
        )}
      </div>
      <p className="text-xs text-zinc-500 mb-4">
        National top searches right now (hourly source, ~4 snapshots/day) — hours ahead of the
        daily snapshot above. Δ compares the current (partial) week's score with last week's.
        Click a term to drill down.
      </p>

      {!data && <div className="h-40 rounded-xl animate-pulse" style={{ background: '#0c0c0f' }} />}
      {data && (data.rows.length > 0 ? (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-x-6 gap-y-1">
          {data.rows.map(r => (
            <button
              key={r.term}
              onClick={() => onSelectTerm(r.term)}
              className="flex items-center gap-3 px-2 py-1.5 rounded-lg text-left hover:bg-zinc-800/30 transition-colors cursor-pointer"
            >
              <span className="text-[10px] font-mono text-zinc-600 w-6 text-right shrink-0">#{r.rank}</span>
              <span className="text-xs text-zinc-300 truncate flex-1">{r.term}</span>
              <WowBadge row={r} />
            </button>
          ))}
        </div>
      ) : (
        <EmptyState text="No hourly snapshot in the last 24 hours" />
      ))}
    </div>
  );
}

function WowBadge({ row }: { row: SemPulseRow }) {
  if (row.prev_week_score === 0) {
    return <span className="text-[10px] font-mono text-violet-400 shrink-0">new</span>;
  }
  const delta = row.score - row.prev_week_score;
  if (delta > 0) return <span className="text-[10px] font-mono text-emerald-400 shrink-0">▲ +{delta} wow</span>;
  if (delta < 0) return <span className="text-[10px] font-mono text-rose-400 shrink-0">▼ {delta} wow</span>;
  return <span className="text-[10px] font-mono text-zinc-600 shrink-0">= wow</span>;
}

// The hourly refresh_time is a UTC wall-clock DATETIME (no zone suffix).
function snapshotAge(snapshotTime: string): string {
  const ms = Date.now() - Date.parse(snapshotTime + 'Z');
  if (Number.isNaN(ms) || ms < 0) return '';
  const hours = ms / 3_600_000;
  if (hours < 1) return `${Math.round(hours * 60)}m ago`;
  return `${Math.round(hours)}h ago`;
}

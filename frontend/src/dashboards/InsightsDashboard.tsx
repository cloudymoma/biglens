import { useState, useEffect } from 'react';
import { Lightbulb, Sparkles, AlertTriangle, DollarSign } from 'lucide-react';
import type { QueryFilters, InsightsDashboardData } from '../types';
import { fetchInsightsDashboard } from '../api';
import { EmptyState, ErrorBanner } from './shared';

export default function InsightsDashboard({ filters }: { filters: QueryFilters }) {
  const [data, setData] = useState<InsightsDashboardData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchInsightsDashboard(filters)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [filters]);

  if (loading) return <LoadingPulse />;
  if (error) return <ErrorBanner message={error} />;
  if (!data) return null;

  const recs = data.recommendations || [];
  const totalSavings = recs.reduce((s, r) => s + (r.projected_savings_usd || 0), 0);
  const isClustering = (r: string) => r.includes('Clustering');

  return (
    <div className="space-y-6">
      {/* Summary bar */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div className="rounded-2xl border border-zinc-800/50 p-5 flex items-center gap-4" style={{ background: '#111114' }}>
          <div className="p-2.5 rounded-xl border border-amber-500/20" style={{ background: '#78350f10' }}>
            <Lightbulb size={20} className="text-amber-400" />
          </div>
          <div>
            <p className="text-2xl font-bold text-white font-mono">{recs.length}</p>
            <p className="text-xs text-zinc-500">Active Recommendations</p>
          </div>
        </div>
        <div className="rounded-2xl border border-zinc-800/50 p-5 flex items-center gap-4" style={{ background: '#111114' }}>
          <div className="p-2.5 rounded-xl border border-emerald-500/20" style={{ background: '#052e1610' }}>
            <DollarSign size={20} className="text-emerald-400" />
          </div>
          <div>
            <p className="text-2xl font-bold text-white font-mono">${totalSavings.toFixed(0)}</p>
            <p className="text-xs text-zinc-500">Projected Savings (USD)</p>
          </div>
        </div>
        <div className="rounded-2xl border border-zinc-800/50 p-5 flex items-center gap-4" style={{ background: '#111114' }}>
          <div className="p-2.5 rounded-xl border border-cyan-500/20" style={{ background: '#0e749010' }}>
            <Sparkles size={20} className="text-cyan-400" />
          </div>
          <div>
            <p className="text-2xl font-bold text-white font-mono">
              {recs.filter(r => isClustering(r.recommender)).length}
            </p>
            <p className="text-xs text-zinc-500">Performance Tuning</p>
          </div>
        </div>
      </div>

      {/* Recommendation feed (Widget 4.1) */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Action Items</h3>
        <p className="text-xs text-zinc-500 mb-5">Active BigQuery recommendations from INFORMATION_SCHEMA</p>
        {recs.length > 0 ? (
          <div className="space-y-3 max-h-[500px] overflow-y-auto pr-2">
            {recs.map((r, i) => (
              <div key={i} className="flex items-start gap-4 p-4 rounded-xl border border-zinc-800/30 hover:border-zinc-700/50 transition-colors" style={{ background: '#09090b' }}>
                <div className={`mt-0.5 p-2 rounded-lg border shrink-0 ${
                  isClustering(r.recommender)
                    ? 'border-emerald-500/20 text-emerald-400'
                    : 'border-amber-500/20 text-amber-400'
                }`} style={{ background: isClustering(r.recommender) ? '#052e1610' : '#78350f10' }}>
                  {isClustering(r.recommender) ? <Sparkles size={16} /> : <AlertTriangle size={16} />}
                </div>
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 mb-1">
                    <span className={`text-[10px] font-semibold px-2 py-0.5 rounded-md border ${
                      isClustering(r.recommender)
                        ? 'text-emerald-400 border-emerald-500/20 bg-emerald-500/5'
                        : 'text-amber-400 border-amber-500/20 bg-amber-500/5'
                    }`}>
                      {isClustering(r.recommender) ? 'Performance Tuning' : r.category || 'Cost'}
                    </span>
                    {r.projected_savings_usd > 0 && (
                      <span className="text-[10px] font-mono text-zinc-500">
                        saves ~${r.projected_savings_usd.toFixed(0)}
                      </span>
                    )}
                  </div>
                  <p className="text-sm text-zinc-300 leading-relaxed">{r.description}</p>
                  <p className="text-[10px] text-zinc-600 mt-1 font-mono truncate">{r.recommender}</p>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <EmptyState text="No active recommendations" />
        )}
      </div>
    </div>
  );
}

function LoadingPulse() {
  return (
    <div className="space-y-4">
      {[1, 2].map(i => (
        <div key={i} className="h-32 rounded-2xl animate-pulse" style={{ background: '#111114' }} />
      ))}
    </div>
  );
}

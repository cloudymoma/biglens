import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { Lightbulb, Sparkles, AlertTriangle, DollarSign, XCircle, Gauge, Repeat } from 'lucide-react';
import type { QueryFilters, InsightsDashboardData, PerfInsightJob } from '../types';
import { fetchInsightsDashboard } from '../api';
import { formatBytes, EmptyState, ErrorBanner } from './shared';

const INSIGHT_FLAGS: { key: keyof PerfInsightJob; label: string }[] = [
  { key: 'slot_contention', label: 'Slot Contention' },
  { key: 'shuffle_quota', label: 'Shuffle Quota' },
  { key: 'high_card_join', label: 'High-Cardinality Join' },
  { key: 'partition_skew', label: 'Partition Skew' },
];

export default function InsightsDashboard({ filters, onDrillToJobs }: {
  filters: QueryFilters;
  onDrillToJobs?: (status: string) => void;
}) {
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

  const errorStats = data.error_stats || [];
  const failingUsers = data.failing_users || [];
  const perfInsights = data.perf_insights || [];
  const repeated = data.repeated_queries || [];

  const failedJobs = errorStats.reduce((s, e) => s + e.job_count, 0);
  const wastedSlotHours = errorStats.reduce((s, e) => s + e.slot_ms, 0) / 3_600_000;
  const insightCounts = INSIGHT_FLAGS.map(f => ({
    ...f,
    count: perfInsights.filter(j => j[f.key] === true).length,
  }));

  const errorDonutOption = {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'item',
      backgroundColor: 'rgba(17,17,20,0.95)',
      borderColor: '#27272a',
      textStyle: { color: '#e4e4e7', fontSize: 12 },
      formatter: (p: any) => {
        const stat = errorStats.find(e => e.reason === p.name);
        const hrs = stat ? (stat.slot_ms / 3_600_000).toFixed(2) : '0';
        return `${p.name}: ${p.value} jobs<br/>${hrs} wasted slot-hours`;
      },
    },
    legend: { bottom: 0, textStyle: { color: '#71717a', fontSize: 11 }, itemWidth: 10, itemHeight: 10 },
    series: [{
      type: 'pie',
      radius: ['50%', '75%'],
      center: ['50%', '42%'],
      label: { show: false },
      data: errorStats.map((e, i) => ({
        name: e.reason,
        value: e.job_count,
        itemStyle: { color: ['#fb7185', '#fbbf24', '#c084fc', '#38bdf8', '#4ade80', '#f472b6', '#94a3b8'][i % 7] },
      })),
    }],
  };

  const wastedBarOption = {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: 'rgba(17,17,20,0.95)',
      borderColor: '#27272a',
      textStyle: { color: '#e4e4e7', fontSize: 12 },
      formatter: (params: any) => `${params[0].name}: ${params[0].value} slot-hours wasted`,
    },
    grid: { left: 8, right: 24, bottom: 8, top: 8, containLabel: true },
    xAxis: {
      type: 'value',
      axisLabel: { color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' },
      splitLine: { lineStyle: { color: '#1f1f23', type: 'dashed' } },
    },
    yAxis: {
      type: 'category',
      data: [...errorStats].reverse().map(e => e.reason),
      axisLabel: { color: '#a1a1aa', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [{
      type: 'bar',
      barMaxWidth: 18,
      data: [...errorStats].reverse().map(e => Number((e.slot_ms / 3_600_000).toFixed(2))),
      itemStyle: { color: '#fb7185', borderRadius: [0, 4, 4, 0] },
    }],
  };

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

      {/* Widget 4.2: Error analysis */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <div className="flex items-center justify-between mb-1">
          <div className="flex items-center gap-2">
            <XCircle size={16} className="text-rose-400" />
            <h3 className="text-sm font-semibold text-white">Error Analysis</h3>
          </div>
          {onDrillToJobs && failedJobs > 0 && (
            <button
              onClick={() => onDrillToJobs('failed')}
              className="text-[11px] px-3 py-1.5 rounded-lg border border-rose-500/20 text-rose-400 hover:bg-rose-500/10 cursor-pointer transition-colors"
            >
              View failed jobs →
            </button>
          )}
        </div>
        <p className="text-xs text-zinc-500 mb-4">
          {failedJobs.toLocaleString()} failed jobs · {wastedSlotHours.toFixed(1)} wasted slot-hours — rank by waste, not by count
        </p>
        {errorStats.length > 0 ? (
          <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
            <div className="h-[260px]">
              <p className="text-[10px] text-zinc-600 uppercase font-semibold tracking-wider mb-2">Failures by reason</p>
              <ReactECharts option={errorDonutOption} style={{ height: '100%' }} />
            </div>
            <div className="h-[260px]">
              <p className="text-[10px] text-zinc-600 uppercase font-semibold tracking-wider mb-2">Wasted slot-hours by reason</p>
              <ReactECharts option={wastedBarOption} style={{ height: '100%' }} />
            </div>
            <div>
              <p className="text-[10px] text-zinc-600 uppercase font-semibold tracking-wider mb-2">Top failing principals</p>
              <div className="space-y-2 max-h-[250px] overflow-y-auto pr-1">
                {failingUsers.map((u, i) => (
                  <div key={i} className="flex items-center justify-between p-2.5 rounded-lg border border-zinc-800/30" style={{ background: '#09090b' }}>
                    <span className="text-xs text-zinc-300 font-mono truncate max-w-[60%]" title={u.user_email}>{u.user_email}</span>
                    <span className="text-[11px] text-zinc-500 font-mono">
                      {u.job_count} fails · {(u.slot_ms / 3_600_000).toFixed(1)} sl-hrs
                    </span>
                  </div>
                ))}
              </div>
            </div>
          </div>
        ) : (
          <EmptyState text="No failed jobs in this window" />
        )}
      </div>

      {/* Widget 4.3: Performance insights */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <div className="flex items-center gap-2 mb-1">
          <Gauge size={16} className="text-amber-400" />
          <h3 className="text-sm font-semibold text-white">Performance Insights</h3>
        </div>
        <p className="text-xs text-zinc-500 mb-4">BigQuery's own diagnosis from query_info.performance_insights</p>
        <div className="flex flex-wrap gap-2 mb-4">
          {insightCounts.map(c => (
            <span key={c.key} className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-medium border ${
              c.count > 0
                ? 'text-amber-400 bg-amber-500/5 border-amber-500/15'
                : 'text-zinc-600 bg-zinc-500/5 border-zinc-800/40'
            }`}>
              {c.label}
              <span className="font-mono">{c.count}</span>
            </span>
          ))}
        </div>
        {perfInsights.length > 0 ? (
          <div className="overflow-x-auto max-h-[300px] overflow-y-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-zinc-500 border-b border-zinc-800/50">
                  <th className="text-left py-2 px-3 font-medium">Job ID</th>
                  <th className="text-left py-2 px-3 font-medium">User</th>
                  <th className="text-right py-2 px-3 font-medium">Slot Time</th>
                  <th className="text-left py-2 px-3 font-medium">Flags</th>
                </tr>
              </thead>
              <tbody>
                {perfInsights.slice(0, 20).map((j, i) => (
                  <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                    <td className="py-2.5 px-3 text-white font-mono max-w-[220px] truncate" title={j.job_id}>{j.job_id}</td>
                    <td className="py-2.5 px-3 text-zinc-400 font-mono max-w-[160px] truncate" title={j.user_email}>{j.user_email}</td>
                    <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">{(j.slot_ms / 1000).toFixed(0)}s</td>
                    <td className="py-2.5 px-3">
                      <div className="flex flex-wrap gap-1">
                        {INSIGHT_FLAGS.filter(f => j[f.key] === true).map(f => (
                          <span key={f.key} className="px-1.5 py-0.5 rounded text-[10px] text-amber-400 bg-amber-500/10">{f.label}</span>
                        ))}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState text="No performance insights flagged in this window" />
        )}
      </div>

      {/* Widget 4.4: Most-repeated queries */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <div className="flex items-center gap-2 mb-1">
          <Repeat size={16} className="text-cyan-400" />
          <h3 className="text-sm font-semibold text-white">Most-Repeated Queries</h3>
        </div>
        <p className="text-xs text-zinc-500 mb-4">Same statement, different literals — loop and scheduling bugs ranked by total bytes billed</p>
        {repeated.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-zinc-500 border-b border-zinc-800/50">
                  <th className="text-left py-2 px-3 font-medium">Sample Query</th>
                  <th className="text-right py-2 px-3 font-medium">Runs</th>
                  <th className="text-right py-2 px-3 font-medium">Users</th>
                  <th className="text-right py-2 px-3 font-medium">Total Billed</th>
                  <th className="text-right py-2 px-3 font-medium">Est. Cost</th>
                </tr>
              </thead>
              <tbody>
                {repeated.map((q, i) => (
                  <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                    <td className="py-2.5 px-3 text-zinc-300 font-mono max-w-[420px] truncate" title={q.sample_query}>{q.sample_query}</td>
                    <td className="py-2.5 px-3 text-right text-white font-mono">{q.runs}</td>
                    <td className="py-2.5 px-3 text-right text-zinc-400 font-mono">{q.user_count}</td>
                    <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">{formatBytes(q.total_bytes)}</td>
                    <td className="py-2.5 px-3 text-right text-amber-400 font-mono">${((q.total_bytes / Math.pow(1024, 4)) * 6.25).toFixed(2)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState text="No repeated queries in this window" />
        )}
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

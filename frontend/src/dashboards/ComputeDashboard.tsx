import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { Activity, TrendingUp, Cpu, Hourglass, Timer, Layers } from 'lucide-react';
import type { QueryFilters, ComputeDashboardData, SlotStatePoint } from '../types';
import { fetchComputeDashboard } from '../api';
import { MetricCard, EmptyState, ErrorBanner } from './shared';

// Pivots (period, state) rows into aligned PENDING/RUNNING series.
function pivotTimeline(points: SlotStatePoint[]) {
  const periods = [...new Set(points.map(p => p.period_start))].sort();
  const byKey = new Map(points.map(p => [`${p.period_start}|${p.state}`, p.slots]));
  return {
    periods,
    running: periods.map(t => Number((byKey.get(`${t}|RUNNING`) || 0).toFixed(1))),
    pending: periods.map(t => Number((byKey.get(`${t}|PENDING`) || 0).toFixed(1))),
  };
}

function fmtMs(ms: number): string {
  if (ms >= 60_000) return `${(ms / 60_000).toFixed(1)}m`;
  if (ms >= 1_000) return `${(ms / 1_000).toFixed(1)}s`;
  return `${Math.round(ms)}ms`;
}

export default function ComputeDashboard({ filters }: { filters: QueryFilters }) {
  const [data, setData] = useState<ComputeDashboardData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchComputeDashboard(filters)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [filters]);

  if (loading) return <LoadingPulse />;
  if (error) return <ErrorBanner message={error} />;
  if (!data) return null;

  const timeline = data.slot_timeline || [];
  const topJobs = data.top_jobs || [];
  const qs = data.queue_stats;
  const reservations = data.reservations || [];

  const { periods, running, pending } = pivotTimeline(timeline);
  const totals = periods.map((_, i) => running[i] + pending[i]);
  const peakConcurrent = totals.length > 0 ? Math.max(...totals) : 0;
  const peakPending = pending.length > 0 ? Math.max(...pending) : 0;

  const fmtTick = (t: string) => {
    const d = new Date(t);
    return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}:${d.getSeconds().toString().padStart(2, '0')}`;
  };

  const axisStyle = {
    axisLabel: { color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' },
    axisLine: { show: false },
    axisTick: { show: false },
  };
  const tooltipStyle = {
    trigger: 'axis',
    backgroundColor: 'rgba(17,17,20,0.95)',
    borderColor: '#27272a',
    textStyle: { color: '#e4e4e7', fontSize: 12 },
  };

  const stackedOption = {
    backgroundColor: 'transparent',
    tooltip: tooltipStyle,
    legend: { top: 0, textStyle: { color: '#71717a', fontSize: 11 }, itemWidth: 10, itemHeight: 10 },
    grid: { left: 48, right: 24, bottom: 32, top: 32 },
    xAxis: {
      type: 'category',
      data: periods.map(fmtTick),
      ...axisStyle,
      axisLabel: { ...axisStyle.axisLabel, interval: Math.max(Math.floor(periods.length / 10), 1) },
    },
    yAxis: {
      type: 'value',
      axisLabel: axisStyle.axisLabel,
      splitLine: { lineStyle: { color: '#1f1f23', type: 'dashed' } },
    },
    series: [
      {
        name: 'Running', type: 'line', stack: 'slots', smooth: true, showSymbol: false,
        data: running,
        lineStyle: { color: '#38bdf8', width: 2 },
        areaStyle: { color: 'rgba(56,189,248,0.25)' },
        itemStyle: { color: '#38bdf8' },
      },
      {
        name: 'Pending', type: 'line', stack: 'slots', smooth: true, showSymbol: false,
        data: pending,
        lineStyle: { color: '#fb7185', width: 2 },
        areaStyle: { color: 'rgba(251,113,133,0.3)' },
        itemStyle: { color: '#fb7185' },
      },
    ],
    dataZoom: [{ type: 'inside', start: 0, end: 100 }],
  };

  const reservationOption = {
    backgroundColor: 'transparent',
    tooltip: tooltipStyle,
    legend: { top: 0, textStyle: { color: '#71717a', fontSize: 11 }, itemWidth: 10, itemHeight: 10 },
    grid: { left: 48, right: 24, bottom: 32, top: 32 },
    xAxis: {
      type: 'category',
      data: reservations.map(r => fmtTick(r.period_start)),
      ...axisStyle,
      axisLabel: { ...axisStyle.axisLabel, interval: Math.max(Math.floor(reservations.length / 10), 1) },
    },
    yAxis: {
      type: 'value',
      axisLabel: axisStyle.axisLabel,
      splitLine: { lineStyle: { color: '#1f1f23', type: 'dashed' } },
    },
    series: [
      {
        name: 'Baseline', type: 'line', showSymbol: false, step: 'end',
        data: reservations.map(r => r.assigned),
        lineStyle: { color: '#4ade80', width: 2, type: 'dashed' },
        itemStyle: { color: '#4ade80' },
      },
      {
        name: 'Baseline + Autoscale', type: 'line', showSymbol: false, step: 'end',
        data: reservations.map(r => Number((r.assigned + r.autoscale).toFixed(0))),
        lineStyle: { color: '#c084fc', width: 2 },
        itemStyle: { color: '#c084fc' },
      },
    ],
    dataZoom: [{ type: 'inside', start: 0, end: 100 }],
  };

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <MetricCard label="Peak Concurrent" value={peakConcurrent.toFixed(0)} icon={<TrendingUp size={18} />} detail="Max running + pending slots" accentColor="#38bdf8" />
        <MetricCard label="Peak Pending" value={peakPending.toFixed(0)} icon={<Hourglass size={18} />} detail="Sustained pending = slot starvation" accentColor="#fb7185" />
        <MetricCard label="Jobs in Window" value={qs ? qs.job_count.toLocaleString() : '---'} icon={<Activity size={18} />} detail="Completed jobs analyzed" accentColor="#4ade80" />
      </div>

      {/* Widget 2.3: Queue & duration KPIs */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <MetricCard label="Avg Queue Time" value={qs ? fmtMs(qs.avg_queue_ms) : '---'} icon={<Hourglass size={18} />} detail="creation → start" accentColor="#fbbf24" />
        <MetricCard label="P95 Queue Time" value={qs ? fmtMs(qs.p95_queue_ms) : '---'} icon={<Hourglass size={18} />} detail="creation → start, 95th pct" accentColor="#fb7185" />
        <MetricCard label="Avg Duration" value={qs ? fmtMs(qs.avg_run_ms) : '---'} icon={<Timer size={18} />} detail="start → end" accentColor="#38bdf8" />
        <MetricCard label="P95 Duration" value={qs ? fmtMs(qs.p95_run_ms) : '---'} icon={<Timer size={18} />} detail="start → end, 95th pct" accentColor="#c084fc" />
      </div>

      {/* Widget 2.1: Pending vs Running stacked concurrency */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Concurrency: Running vs Pending</h3>
        <p className="text-xs text-zinc-500 mb-4">Stacked slot-seconds from JOBS_TIMELINE — a growing red band means jobs are queueing for slots</p>
        {periods.length > 0 ? (
          <div className="h-[340px]">
            <ReactECharts option={stackedOption} style={{ height: '100%' }} />
          </div>
        ) : (
          <EmptyState text="No slot timeline data available" />
        )}
      </div>

      {/* Widget 2.4: Reservation utilization */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <div className="flex items-center gap-2 mb-1">
          <Layers size={16} className="text-purple-400" />
          <h3 className="text-sm font-semibold text-white">Reservation Capacity</h3>
        </div>
        <p className="text-xs text-zinc-500 mb-4">Baseline and autoscaled slots from RESERVATIONS_TIMELINE — compare against the concurrency chart above</p>
        {reservations.length > 0 ? (
          <div className="h-[300px]">
            <ReactECharts option={reservationOption} style={{ height: '100%' }} />
          </div>
        ) : (
          <EmptyState text="No reservations — this project runs on-demand" />
        )}
      </div>

      {/* Widget 2.2: Slot Gluttons */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <div className="flex items-center gap-2 mb-1">
          <Cpu size={16} className="text-rose-400" />
          <h3 className="text-sm font-semibold text-white">Slot Gluttons</h3>
        </div>
        <p className="text-xs text-zinc-500 mb-4">Top 10 jobs by cumulative slot time</p>
        {topJobs.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-zinc-500 border-b border-zinc-800/50">
                  <th className="text-left py-2 px-3 font-medium">#</th>
                  <th className="text-left py-2 px-3 font-medium">Job ID</th>
                  <th className="text-left py-2 px-3 font-medium">User</th>
                  <th className="text-right py-2 px-3 font-medium">Slot Time</th>
                  <th className="text-right py-2 px-3 font-medium">Duration</th>
                  <th className="text-left py-2 px-3 font-medium">State</th>
                  <th className="text-left py-2 px-3 font-medium">Cache</th>
                  <th className="text-left py-2 px-3 font-medium">Reservation</th>
                </tr>
              </thead>
              <tbody>
                {topJobs.map((j, i) => (
                  <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                    <td className="py-2.5 px-3 text-zinc-600 font-mono">{i + 1}</td>
                    <td className="py-2.5 px-3 text-white font-mono max-w-[220px] truncate" title={j.job_id}>{j.job_id}</td>
                    <td className="py-2.5 px-3 text-zinc-400 font-mono max-w-[180px] truncate" title={j.user_email}>{j.user_email}</td>
                    <td className="py-2.5 px-3 text-right text-rose-400 font-mono">{(j.total_slot_ms / 1000).toLocaleString(undefined, { maximumFractionDigits: 0 })}s</td>
                    <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">{fmtMs(j.duration_ms)}</td>
                    <td className="py-2.5 px-3 text-zinc-400 font-mono">{j.state}</td>
                    <td className="py-2.5 px-3 text-zinc-400 font-mono">{j.cache_hit ? 'HIT' : '—'}</td>
                    <td className="py-2.5 px-3 text-zinc-400 font-mono max-w-[140px] truncate" title={j.reservation}>{j.reservation || 'on-demand'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState text="No job slot data available" />
        )}
      </div>
    </div>
  );
}

function LoadingPulse() {
  return (
    <div className="space-y-4">
      {[1, 2, 3].map(i => (
        <div key={i} className="h-32 rounded-2xl animate-pulse" style={{ background: '#111114' }} />
      ))}
    </div>
  );
}

import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { Activity, TrendingUp, Cpu } from 'lucide-react';
import type { QueryFilters, ComputeDashboardData } from '../types';
import { fetchComputeDashboard } from '../api';
import { MetricCard, EmptyState, ErrorBanner } from './shared';

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
  const slotUsage = data.slot_usage || [];

  const peakConcurrent = timeline.length > 0 ? Math.max(...timeline.map(s => s.concurrent_slots)) : 0;
  const peakHourly = slotUsage.length > 0 ? Math.max(...slotUsage.map(s => s.avg_slots)) : 0;
  const avgHourly = slotUsage.length > 0 ? slotUsage.reduce((s, u) => s + u.avg_slots, 0) / slotUsage.length : 0;

  const areaOption = {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: 'rgba(17,17,20,0.95)',
      borderColor: '#27272a',
      textStyle: { color: '#e4e4e7', fontSize: 12 },
    },
    grid: { left: 48, right: 24, bottom: 32, top: 24 },
    xAxis: {
      type: 'category',
      data: timeline.map(s => {
        const d = new Date(s.period_start);
        return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}:${d.getSeconds().toString().padStart(2, '0')}`;
      }),
      axisLabel: {
        color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace',
        interval: Math.max(Math.floor(timeline.length / 10), 1),
      },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    yAxis: {
      type: 'value',
      axisLabel: { color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' },
      splitLine: { lineStyle: { color: '#1f1f23', type: 'dashed' } },
    },
    series: [{
      name: 'Concurrent Slots',
      data: timeline.map(s => Number(s.concurrent_slots.toFixed(1))),
      type: 'line',
      smooth: true,
      showSymbol: false,
      lineStyle: { color: '#38bdf8', width: 2 },
      areaStyle: {
        color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [
          { offset: 0, color: 'rgba(56,189,248,0.3)' },
          { offset: 1, color: 'rgba(56,189,248,0)' },
        ]},
      },
    }],
    dataZoom: [{ type: 'inside', start: 0, end: 100 }],
  };

  const gluttonOption = {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: 'rgba(17,17,20,0.95)',
      borderColor: '#27272a',
      textStyle: { color: '#e4e4e7', fontSize: 12 },
      formatter: (params: any) => {
        const p = params[0];
        const ms = p.value;
        const sec = (ms / 1000).toFixed(1);
        return `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${p.name}</div>
                <div style="color:#f4f4f5;font-size:13px;font-weight:600">${Number(sec).toLocaleString()}s slot-time</div>`;
      },
    },
    grid: { left: 48, right: 24, bottom: 60, top: 16 },
    xAxis: {
      type: 'category',
      data: topJobs.map(j => j.job_id.length > 20 ? j.job_id.slice(0, 18) + '...' : j.job_id),
      axisLabel: { color: '#71717a', fontSize: 9, fontFamily: 'JetBrains Mono', rotate: 35 },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    yAxis: {
      type: 'value',
      axisLabel: {
        color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono',
        formatter: (v: number) => v >= 1e6 ? `${(v / 1e6).toFixed(1)}M` : v >= 1e3 ? `${(v / 1e3).toFixed(0)}k` : v.toString(),
      },
      splitLine: { lineStyle: { color: '#1f1f23', type: 'dashed' } },
    },
    series: [{
      data: topJobs.map(j => j.total_slot_ms),
      type: 'bar',
      barMaxWidth: 24,
      itemStyle: {
        color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [
          { offset: 0, color: '#fb7185' },
          { offset: 1, color: '#9f1239' },
        ]},
        borderRadius: [4, 4, 0, 0],
      },
    }],
  };

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <MetricCard label="Peak Concurrent" value={peakConcurrent.toFixed(0)} icon={<TrendingUp size={18} />} detail="Max concurrent slots" accentColor="#fb7185" />
        <MetricCard label="Peak Hourly Avg" value={peakHourly.toFixed(1)} icon={<Cpu size={18} />} detail="Max hourly slot average" accentColor="#38bdf8" />
        <MetricCard label="Avg Hourly Slots" value={avgHourly.toFixed(1)} icon={<Activity size={18} />} detail={`Over ${slotUsage.length} data points`} accentColor="#4ade80" />
      </div>

      {/* Widget 2.1: Concurrent Slot Usage Area Chart */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Concurrent Slot Usage</h3>
        <p className="text-xs text-zinc-500 mb-4">Second-level concurrency from JOBS_TIMELINE</p>
        {timeline.length > 0 ? (
          <div className="h-[340px]">
            <ReactECharts option={areaOption} style={{ height: '100%' }} />
          </div>
        ) : (
          <EmptyState text="No slot timeline data available" />
        )}
      </div>

      {/* Widget 2.2: Slot Gluttons */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Slot Gluttons</h3>
        <p className="text-xs text-zinc-500 mb-4">Top 10 jobs by cumulative slot time</p>
        {topJobs.length > 0 ? (
          <div className="h-[320px]">
            <ReactECharts option={gluttonOption} style={{ height: '100%' }} />
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

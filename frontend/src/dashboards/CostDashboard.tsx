import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { DollarSign, TrendingUp, Scale, GitCompareArrows } from 'lucide-react';
import type { QueryFilters, CostDashboardData } from '../types';
import { fetchCostDashboard } from '../api';
import { formatBytes, MetricCard, EmptyState, ErrorBanner } from './shared';
import { getRegionPricing, EDITION_LABELS, TIB, type Edition } from './pricing';

const GROUP_LABELS: Record<string, string> = {
  user: 'User', dataset: 'Dataset', table: 'Table', reservation: 'Reservation',
};

export default function CostDashboard({ filters }: { filters: QueryFilters }) {
  const [data, setData] = useState<CostDashboardData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [edition, setEdition] = useState<Edition>('enterprise');

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError('');
    fetchCostDashboard(filters)
      .then(d => { if (active) setData(d); })
      .catch(e => { if (active) setError(e.response?.data || e.message); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [filters]);

  if (loading) return <LoadingPulse />;
  if (error) return <ErrorBanner message={error} />;
  if (!data) return null;

  const bytesBilled = data.summary?.bytes_billed || 0;
  const onDemandBytesBilled = data.summary?.ondemand_bytes_billed ?? bytesBilled;
  const bytesProcessed = data.summary?.bytes_processed || 0;
  const slotMs = data.summary?.total_slot_ms || 0;
  const spendBy = data.spend_by || [];
  const dailyCost = data.daily_cost || [];

  const { onDemandPerTiB, slotHourRates } = getRegionPricing(filters.region);
  const onDemandCost = (onDemandBytesBilled / TIB) * onDemandPerTiB;
  const allOnDemandWhatIfCost = (bytesBilled / TIB) * onDemandPerTiB;
  const slotHours = slotMs / 3_600_000;
  const editionCost = slotHours * slotHourRates[edition];
  const editionsCheaper = editionCost < allOnDemandWhatIfCost;
  const gapPct = bytesProcessed > 0 ? ((bytesBilled - bytesProcessed) / bytesProcessed) * 100 : 0;

  const groupLabel = GROUP_LABELS[filters.group_by] || 'User';
  const totalSpendBytes = spendBy.reduce((s, u) => s + u.total_bytes, 0);
  const denomBytes = bytesBilled > 0 ? bytesBilled : totalSpendBytes;

  const treemapOption = {
    backgroundColor: 'transparent',
    tooltip: {
      backgroundColor: 'rgba(17,17,20,0.95)',
      borderColor: '#27272a',
      textStyle: { color: '#e4e4e7', fontSize: 12 },
      formatter: (p: any) => {
        const pct = denomBytes > 0 ? ((p.value / denomBytes) * 100).toFixed(1) : '0';
        return `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${p.name}</div>
                <div style="color:#f4f4f5;font-size:13px">${formatBytes(p.value)}</div>
                <div style="color:#71717a;font-size:11px">${pct}% of project billed bytes</div>`;
      },
    },
    series: [{
      type: 'treemap',
      width: '100%',
      height: '100%',
      roam: false,
      nodeClick: false,
      breadcrumb: { show: false },
      label: {
        show: true,
        color: '#f4f4f5',
        fontSize: 11,
        fontFamily: 'JetBrains Mono, monospace',
        formatter: (p: any) => {
          const name = p.name;
          const short = name.includes('@') ? name.split('@')[0] : name;
          return short.length > 14 ? short.slice(0, 12) + '..' : short;
        },
      },
      levels: [{
        itemStyle: { borderColor: '#09090b', borderWidth: 3, gapWidth: 3 },
        colorMappingBy: 'value',
      }],
      data: spendBy.map((u, i) => ({
        name: u.name,
        value: u.total_bytes,
        itemStyle: {
          color: [
            '#0e7490', '#7c3aed', '#0f766e', '#b45309', '#be185d',
            '#1d4ed8', '#15803d', '#9333ea', '#c2410c', '#4338ca',
          ][i % 10],
        },
      })),
    }],
  };

  const dailyOption = {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: 'rgba(17,17,20,0.95)',
      borderColor: '#27272a',
      textStyle: { color: '#e4e4e7', fontSize: 12 },
      formatter: (params: any) => {
        const p = params[0];
        const usd = (p.value / TIB) * onDemandPerTiB;
        const isPartial = dailyCost.length > 1 && (p.dataIndex === 0 || p.dataIndex === dailyCost.length - 1);
        const partialNote = isPartial ? ' <span style="color:#fbbf24">(partial UTC day)</span>' : '';
        return `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${p.name}${partialNote}</div>
                <div style="color:#f4f4f5;font-size:13px">${formatBytes(p.value)} · ~$${usd.toFixed(2)}</div>`;
      },
    },
    grid: { left: 56, right: 24, bottom: 32, top: 24 },
    xAxis: {
      type: 'category',
      data: dailyCost.map(d => d.day),
      axisLabel: { color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    yAxis: {
      type: 'value',
      axisLabel: {
        color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace',
        formatter: (v: number) => formatBytes(v),
      },
      splitLine: { lineStyle: { color: '#1f1f23', type: 'dashed' } },
    },
    series: [{
      data: dailyCost.map(d => d.bytes_billed),
      type: 'line',
      smooth: true,
      showSymbol: dailyCost.length < 32,
      lineStyle: { color: '#fbbf24', width: 2 },
      itemStyle: { color: '#fbbf24' },
      areaStyle: {
        color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [
          { offset: 0, color: 'rgba(251,191,36,0.25)' },
          { offset: 1, color: 'rgba(251,191,36,0)' },
        ]},
      },
    }],
  };

  return (
    <div className="space-y-6">
      {/* Widget 3.1: Cost KPIs */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <MetricCard
          label="Data Scanned (Billed)"
          value={formatBytes(bytesBilled)}
          icon={<TrendingUp size={18} />}
          detail={`${(bytesBilled / TIB).toFixed(4)} TiB total (${(onDemandBytesBilled / TIB).toFixed(4)} TiB on-demand)`}
          accentColor="#38bdf8"
        />
        <MetricCard
          label="Estimated On-Demand Cost"
          value={`$${onDemandCost.toFixed(2)}`}
          icon={<DollarSign size={18} />}
          detail={`Unreserved jobs @ $${onDemandPerTiB}/TiB list rate (before 1 TiB/mo free tier)`}
          accentColor="#fbbf24"
        />
        <MetricCard
          label="Billed vs Processed"
          value={`${gapPct >= 0 ? '+' : ''}${gapPct.toFixed(1)}%`}
          icon={<Scale size={18} />}
          detail={`${formatBytes(bytesProcessed)} processed — gap is the 10MB-per-table minimum tax`}
          accentColor={gapPct > 10 ? '#fb7185' : '#4ade80'}
        />
      </div>

      {/* Widget 3.4: On-demand vs Editions what-if */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <div className="flex items-center justify-between mb-1">
          <div className="flex items-center gap-2">
            <GitCompareArrows size={16} className="text-emerald-400" />
            <h3 className="text-sm font-semibold text-white">On-demand vs Editions What-if</h3>
          </div>
          <select
            value={edition}
            onChange={e => setEdition(e.target.value as Edition)}
            className="text-xs text-zinc-300 rounded-lg px-3 py-1.5 outline-none cursor-pointer border border-zinc-800/50"
            style={{ background: '#09090b' }}
          >
            {(Object.keys(slotHourRates) as Edition[]).map(ed => (
              <option key={ed} value={ed}>{EDITION_LABELS[ed]} (${slotHourRates[ed]}/slot-hr)</option>
            ))}
          </select>
        </div>
        <p className="text-xs text-zinc-500 mb-5">
          The same filtered workload priced both ways. Editions PAYG shown is a 100%-utilization theoretical lower bound (actual autoscaling rounds up in 50-slot increments with a 1-minute minimum).
        </p>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
          <div className="p-4 rounded-xl border border-zinc-800/30" style={{ background: '#09090b' }}>
            <p className="text-[10px] text-zinc-600 uppercase font-semibold tracking-wider mb-1">On-demand (All Bytes)</p>
            <p className="text-xl font-bold text-white font-mono">${allOnDemandWhatIfCost.toFixed(2)}</p>
            <p className="text-[11px] text-zinc-600 mt-1">{(bytesBilled / TIB).toFixed(3)} TiB × ${onDemandPerTiB}</p>
          </div>
          <div className="p-4 rounded-xl border border-zinc-800/30" style={{ background: '#09090b' }}>
            <p className="text-[10px] text-zinc-600 uppercase font-semibold tracking-wider mb-1">{EDITION_LABELS[edition]} (100% Util. Bound)</p>
            <p className="text-xl font-bold text-white font-mono">${editionCost.toFixed(2)}</p>
            <p className="text-[11px] text-zinc-600 mt-1">{slotHours.toFixed(1)} slot-hrs × ${slotHourRates[edition]}</p>
          </div>
          <div className="p-4 rounded-xl border" style={{ background: '#09090b', borderColor: editionsCheaper ? '#4ade8030' : '#38bdf830' }}>
            <p className="text-[10px] text-zinc-600 uppercase font-semibold tracking-wider mb-1">Verdict (Ideal Lower Bound)</p>
            <p className="text-xl font-bold font-mono" style={{ color: editionsCheaper ? '#4ade80' : '#38bdf8' }}>
              {editionsCheaper ? EDITION_LABELS[edition] : 'On-demand'}
            </p>
            <p className="text-[11px] text-zinc-600 mt-1">
              cheaper by ${Math.abs(allOnDemandWhatIfCost - editionCost).toFixed(2)} at 100% slot utilization
            </p>
          </div>
        </div>
      </div>

      {/* Widget 3.3: Daily cost trend */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Daily Spend Trend</h3>
        <p className="text-xs text-zinc-500 mb-4">Bytes billed per UTC day (rolling window — first and current UTC days are partial)</p>
        {dailyCost.length > 0 ? (
          <div className="h-[280px]">
            <ReactECharts option={dailyOption} style={{ height: '100%' }} />
          </div>
        ) : (
          <EmptyState text="No daily cost data available" />
        )}
      </div>

      {/* Widget 3.2: Spend treemap (honors Group Top-N By filter) */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Spend by {groupLabel}</h3>
        <p className="text-xs text-zinc-500 mb-4">
          Top 25 by bytes billed per {groupLabel.toLowerCase()}{filters.group_by === 'dataset' || filters.group_by === 'table'
            ? ' — a job referencing N tables counts once under each (group sum can exceed 100% of project bytes)' : ''}
        </p>
        {spendBy.length > 0 ? (
          <div className="h-[380px]">
            <ReactECharts option={treemapOption} style={{ height: '100%' }} />
          </div>
        ) : (
          <EmptyState text="No spend data available" />
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

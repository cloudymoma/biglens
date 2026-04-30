import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { DollarSign, TrendingUp, Users } from 'lucide-react';
import type { QueryFilters, CostDashboardData } from '../types';
import { fetchCostDashboard } from '../api';
import { formatBytes, MetricCard, EmptyState, ErrorBanner } from './shared';

const ON_DEMAND_RATE = 6.25;

export default function CostDashboard({ filters }: { filters: QueryFilters }) {
  const [data, setData] = useState<CostDashboardData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchCostDashboard(filters)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [filters]);

  if (loading) return <LoadingPulse />;
  if (error) return <ErrorBanner message={error} />;
  if (!data) return null;

  const bytesBilled = data.summary?.bytes_billed || 0;
  const tibBilled = bytesBilled / Math.pow(1024, 4);
  const estimatedCost = tibBilled * ON_DEMAND_RATE;
  const spendByUser = data.spend_by_user || [];

  const totalUserBytes = spendByUser.reduce((s, u) => s + u.total_bytes, 0);

  const treemapOption = {
    backgroundColor: 'transparent',
    tooltip: {
      backgroundColor: 'rgba(17,17,20,0.95)',
      borderColor: '#27272a',
      textStyle: { color: '#e4e4e7', fontSize: 12 },
      formatter: (p: any) => {
        const pct = totalUserBytes > 0 ? ((p.value / totalUserBytes) * 100).toFixed(1) : '0';
        return `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${p.name}</div>
                <div style="color:#f4f4f5;font-size:13px">${formatBytes(p.value)}</div>
                <div style="color:#71717a;font-size:11px">${pct}% of total</div>`;
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
          const email = p.name;
          const short = email.includes('@') ? email.split('@')[0] : email;
          return short.length > 12 ? short.slice(0, 10) + '..' : short;
        },
      },
      levels: [{
        itemStyle: {
          borderColor: '#09090b',
          borderWidth: 3,
          gapWidth: 3,
        },
        colorMappingBy: 'value',
      }],
      data: spendByUser.map((u, i) => ({
        name: u.user_email,
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

  return (
    <div className="space-y-6">
      {/* Widget 3.1: Cost KPIs */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <MetricCard
          label="Data Scanned"
          value={formatBytes(bytesBilled)}
          icon={<TrendingUp size={18} />}
          detail={`${tibBilled.toFixed(4)} TiB billed`}
          accentColor="#38bdf8"
        />
        <MetricCard
          label="Estimated Cost"
          value={`$${estimatedCost.toFixed(2)}`}
          icon={<DollarSign size={18} />}
          detail={`On-demand @ $${ON_DEMAND_RATE}/TiB`}
          accentColor="#fbbf24"
        />
        <MetricCard
          label="Active Users"
          value={spendByUser.length.toString()}
          icon={<Users size={18} />}
          detail="Unique query users"
          accentColor="#c084fc"
        />
      </div>

      {/* Widget 3.2: Spend by User Treemap */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Spend by User</h3>
        <p className="text-xs text-zinc-500 mb-4">Bytes billed per user / service account</p>
        {spendByUser.length > 0 ? (
          <div className="h-[380px]">
            <ReactECharts option={treemapOption} style={{ height: '100%' }} />
          </div>
        ) : (
          <EmptyState text="No user spend data available" />
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

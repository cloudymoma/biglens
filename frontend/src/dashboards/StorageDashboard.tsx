import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { Database, HardDrive, Box, Search } from 'lucide-react';
import type { QueryFilters, StorageDashboardData } from '../types';
import { fetchStorageDashboard } from '../api';
import { formatBytes, MetricCard, EmptyState, ErrorBanner } from './shared';

const LOGICAL_RATE = 0.02;
const PHYSICAL_RATE = 0.04;

export default function StorageDashboard({ filters }: { filters: QueryFilters }) {
  const [data, setData] = useState<StorageDashboardData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchStorageDashboard(filters)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [filters]);

  if (loading) return <LoadingPulse />;
  if (error) return <ErrorBanner message={error} />;
  if (!data) return null;

  const billing = data.billing;
  const breakdown = data.breakdown;
  const topTables = data.top_tables || [];
  const searchIndexes = data.search_indexes || [];

  const logicalCost = billing ? (billing.logical_bytes / Math.pow(1024, 4)) * LOGICAL_RATE * 1000 : 0;
  const physicalCost = billing ? (billing.physical_bytes / Math.pow(1024, 4)) * PHYSICAL_RATE * 1000 : 0;
  const savings = Math.abs(logicalCost - physicalCost);
  const cheaperModel = logicalCost <= physicalCost ? 'Logical' : 'Physical';

  const donutOption = {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'item',
      backgroundColor: 'rgba(17,17,20,0.95)',
      borderColor: '#27272a',
      textStyle: { color: '#e4e4e7', fontSize: 12 },
      formatter: (p: any) => `${p.name}: ${formatBytes(p.value)}`,
    },
    legend: {
      bottom: 0,
      textStyle: { color: '#71717a', fontSize: 11 },
      itemWidth: 10,
      itemHeight: 10,
      itemGap: 20,
    },
    series: [{
      type: 'pie',
      radius: ['50%', '75%'],
      center: ['50%', '45%'],
      avoidLabelOverlap: false,
      label: { show: false },
      data: [
        { value: breakdown?.active_bytes || 0, name: 'Active', itemStyle: { color: '#38bdf8' } },
        { value: breakdown?.long_term_bytes || 0, name: 'Long-term', itemStyle: { color: '#c084fc' } },
      ],
      emphasis: {
        itemStyle: { shadowBlur: 10, shadowColor: 'rgba(56,189,248,0.3)' },
      },
    }],
  };

  return (
    <div className="space-y-6">
      {/* Billing Simulator (Widget 1.1) */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <MetricCard
          label="Logical Storage"
          value={billing ? formatBytes(billing.logical_bytes) : '---'}
          icon={<Database size={18} />}
          detail={`~$${logicalCost.toFixed(2)}/mo at logical rate`}
          accentColor="#38bdf8"
        />
        <MetricCard
          label="Physical Storage"
          value={billing ? formatBytes(billing.physical_bytes) : '---'}
          icon={<HardDrive size={18} />}
          detail={`~$${physicalCost.toFixed(2)}/mo at physical rate`}
          accentColor="#c084fc"
        />
        <MetricCard
          label="Recommended Model"
          value={cheaperModel}
          icon={<Box size={18} />}
          detail={`Saves ~$${savings.toFixed(2)}/mo`}
          accentColor="#4ade80"
        />
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        {/* Active vs Long-Term Donut (Widget 1.2) */}
        <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
          <h3 className="text-sm font-semibold text-white mb-1">Storage Breakdown</h3>
          <p className="text-xs text-zinc-500 mb-4">Active vs. long-term logical bytes</p>
          {(breakdown?.active_bytes || breakdown?.long_term_bytes) ? (
            <div className="h-[260px]">
              <ReactECharts option={donutOption} style={{ height: '100%' }} />
            </div>
          ) : (
            <EmptyState text="No storage breakdown data" />
          )}
        </div>

        {/* Top 10 Tables (Widget 1.3) */}
        <div className="lg:col-span-2 rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
          <h3 className="text-sm font-semibold text-white mb-1">Heaviest Tables</h3>
          <p className="text-xs text-zinc-500 mb-4">Top 10 by total logical bytes</p>
          {topTables.length > 0 ? (
            <div className="overflow-x-auto">
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-zinc-500 border-b border-zinc-800/50">
                    <th className="text-left py-2 px-3 font-medium">#</th>
                    <th className="text-left py-2 px-3 font-medium">Dataset</th>
                    <th className="text-left py-2 px-3 font-medium">Table</th>
                    <th className="text-right py-2 px-3 font-medium">Size</th>
                    <th className="text-right py-2 px-3 font-medium">Bar</th>
                  </tr>
                </thead>
                <tbody>
                  {topTables.map((t, i) => {
                    const maxBytes = topTables[0]?.total_bytes || 1;
                    const pct = (t.total_bytes / maxBytes) * 100;
                    return (
                      <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                        <td className="py-2.5 px-3 text-zinc-600 font-mono">{i + 1}</td>
                        <td className="py-2.5 px-3 text-zinc-400 font-mono">{t.dataset}</td>
                        <td className="py-2.5 px-3 text-white font-mono">{t.table_name}</td>
                        <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">{formatBytes(t.total_bytes)}</td>
                        <td className="py-2.5 px-3 w-32">
                          <div className="w-full h-1.5 rounded-full bg-zinc-800">
                            <div className="h-full rounded-full" style={{ width: `${pct}%`, background: 'linear-gradient(90deg, #0e7490, #38bdf8)' }} />
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          ) : (
            <EmptyState text="No table data available" />
          )}
        </div>
      </div>

      {/* Search Indexes (Widget 1.4) */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <div className="flex items-center gap-2 mb-1">
          <Search size={16} className="text-cyan-400" />
          <h3 className="text-sm font-semibold text-white">Search Indexes</h3>
        </div>
        <p className="text-xs text-zinc-500 mb-4">Search indexes configuration, status and storage size</p>
        {searchIndexes.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-zinc-500 border-b border-zinc-800/50">
                  <th className="text-left py-2 px-3 font-medium">#</th>
                  <th className="text-left py-2 px-3 font-medium">Dataset</th>
                  <th className="text-left py-2 px-3 font-medium">Table</th>
                  <th className="text-left py-2 px-3 font-medium">Index Name</th>
                  <th className="text-left py-2 px-3 font-medium">Status</th>
                  <th className="text-left py-2 px-3 font-medium">Coverage</th>
                  <th className="text-right py-2 px-3 font-medium text-nowrap">Logical Size</th>
                  <th className="text-right py-2 px-3 font-medium text-nowrap">Billing Storage Size</th>
                </tr>
              </thead>
              <tbody>
                {searchIndexes.map((idx, i) => {
                  const statusColors: Record<string, string> = {
                    ACTIVE: 'text-emerald-400 bg-emerald-500/5 border-emerald-500/15',
                    PENDING: 'text-amber-400 bg-amber-500/5 border-amber-500/15',
                    TEMPORARILY_DISABLED: 'text-rose-400 bg-rose-500/5 border-rose-500/15',
                  };
                  const statusStyle = statusColors[idx.index_status] || 'text-zinc-400 bg-zinc-500/5 border-zinc-500/15';

                  return (
                    <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                      <td className="py-2.5 px-3 text-zinc-600 font-mono">{i + 1}</td>
                      <td className="py-2.5 px-3 text-zinc-400 font-mono">{idx.dataset}</td>
                      <td className="py-2.5 px-3 text-white font-mono">{idx.table_name}</td>
                      <td className="py-2.5 px-3 text-cyan-400 font-mono">{idx.index_name}</td>
                      <td className="py-2.5 px-3">
                        <span className={`inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium border ${statusStyle}`}>
                          {idx.index_status}
                        </span>
                      </td>
                      <td className="py-2.5 px-3">
                        <div className="flex items-center gap-2">
                          <span className="text-zinc-300 font-mono w-8 text-right">{idx.coverage_percentage}%</span>
                          <div className="w-20 h-1.5 rounded-full bg-zinc-800">
                            <div className="h-full rounded-full" style={{ width: `${idx.coverage_percentage}%`, background: 'linear-gradient(90deg, #06b6d4, #3b82f6)' }} />
                          </div>
                        </div>
                      </td>
                      <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">{formatBytes(idx.total_logical_bytes)}</td>
                      <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">{formatBytes(idx.total_storage_bytes)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState text="No search indexes found" />
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

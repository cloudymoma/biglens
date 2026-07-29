import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { Database, HardDrive, Box, Search, History, ShieldAlert, Snowflake } from 'lucide-react';
import type { QueryFilters, StorageDashboardData } from '../types';
import { fetchStorageDashboard } from '../api';
import { formatBytes, MetricCard, EmptyState, ErrorBanner, DegradedNotice } from './shared';
import { logicalCostUSD, physicalCostUSD, STORAGE_RATES } from './pricing';

export default function StorageDashboard({ filters }: { filters: QueryFilters }) {
  const [data, setData] = useState<StorageDashboardData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError('');
    fetchStorageDashboard(filters)
      .then(d => { if (active) setData(d); })
      .catch(e => { if (active) setError(e.response?.data || e.message); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [filters]);

  if (loading) return <LoadingPulse />;
  if (error) return <ErrorBanner message={error} />;
  if (!data) return null;

  const billing = data.billing;
  const breakdown = data.breakdown;
  const topTables = data.top_tables || [];
  const searchIndexes = data.search_indexes || [];
  const datasetStorage = data.dataset_storage || [];
  const coldTables = data.cold_tables || [];

  // Tiered cost math over the per-dataset rows: active vs long-term rates,
  // and time-travel + fail-safe billed at the active-physical rate.
  const totals = datasetStorage.reduce((a, d) => ({
    activeLogical: a.activeLogical + d.active_logical,
    longTermLogical: a.longTermLogical + d.long_term_logical,
    activePhysical: a.activePhysical + d.active_physical,
    longTermPhysical: a.longTermPhysical + d.long_term_physical,
    timeTravel: a.timeTravel + d.time_travel,
    failSafe: a.failSafe + d.fail_safe,
  }), { activeLogical: 0, longTermLogical: 0, activePhysical: 0, longTermPhysical: 0, timeTravel: 0, failSafe: 0 });

  const logicalCost = logicalCostUSD(totals.activeLogical, totals.longTermLogical);
  const physicalCost = physicalCostUSD(totals.activePhysical, totals.longTermPhysical, totals.failSafe);
  const savings = Math.abs(logicalCost - physicalCost);
  const cheaperModel = logicalCost <= physicalCost ? 'Logical' : 'Physical';

  const totalPhysical = totals.activePhysical + totals.longTermPhysical + totals.failSafe;
  const ttShare = totalPhysical > 0 ? (totals.timeTravel / totalPhysical) * 100 : 0;
  const fsShare = totalPhysical > 0 ? (totals.failSafe / totalPhysical) * 100 : 0;

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
      <DegradedNotice widgets={data.degraded_widgets} />
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
          detail={`Saves ~$${savings.toFixed(2)}/mo (tiered rates, incl. TT+FS)`}
          accentColor="#4ade80"
        />
      </div>

      {/* Time-travel & fail-safe overhead */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <MetricCard
          label="Time Travel Storage"
          value={formatBytes(totals.timeTravel)}
          icon={<History size={18} />}
          detail={`${ttShare.toFixed(1)}% of physical bytes — billed at active-physical rate under physical billing`}
          accentColor="#fbbf24"
        />
        <MetricCard
          label="Fail-safe Storage"
          value={formatBytes(totals.failSafe)}
          icon={<ShieldAlert size={18} />}
          detail={`${fsShare.toFixed(1)}% of physical bytes — churn-heavy data makes physical billing backfire`}
          accentColor="#fb7185"
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

      {/* Per-dataset billing model recommendation (Widget 1.5) */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Billing Model by Dataset</h3>
        <p className="text-xs text-zinc-500 mb-4">
          Logical vs. physical monthly cost per dataset — physical includes time-travel and fail-safe at the active rate
        </p>
        {datasetStorage.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-zinc-500 border-b border-zinc-800/50">
                  <th className="text-left py-2 px-3 font-medium">Dataset</th>
                  <th className="text-right py-2 px-3 font-medium">Logical</th>
                  <th className="text-right py-2 px-3 font-medium">Physical</th>
                  <th className="text-right py-2 px-3 font-medium">Compression</th>
                  <th className="text-right py-2 px-3 font-medium">Logical $/mo</th>
                  <th className="text-right py-2 px-3 font-medium">Physical $/mo</th>
                  <th className="text-right py-2 px-3 font-medium">Best Model</th>
                </tr>
              </thead>
              <tbody>
                {datasetStorage.map((d, i) => {
                  const logical = d.active_logical + d.long_term_logical;
                  const physical = d.active_physical + d.long_term_physical;
                  const lCost = logicalCostUSD(d.active_logical, d.long_term_logical);
                  const pCost = physicalCostUSD(d.active_physical, d.long_term_physical, d.fail_safe);
                  const ratio = physical > 0 ? logical / physical : 0;
                  const physicalWins = pCost < lCost;
                  return (
                    <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                      <td className="py-2.5 px-3 text-white font-mono">{d.dataset}</td>
                      <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">{formatBytes(logical)}</td>
                      <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">{formatBytes(physical)}</td>
                      <td className="py-2.5 px-3 text-right text-zinc-400 font-mono">{ratio > 0 ? `${ratio.toFixed(1)}x` : '--'}</td>
                      <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">${lCost.toFixed(2)}</td>
                      <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">${pCost.toFixed(2)}</td>
                      <td className="py-2.5 px-3 text-right">
                        <span className={`inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium border ${
                          physicalWins
                            ? 'text-purple-400 bg-purple-500/5 border-purple-500/15'
                            : 'text-cyan-400 bg-cyan-500/5 border-cyan-500/15'
                        }`}>
                          {physicalWins ? 'PHYSICAL' : 'LOGICAL'}
                        </span>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState text="No dataset storage data" />
        )}
      </div>

      {/* Cold tables (Widget 1.6) */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <div className="flex items-center gap-2 mb-1">
          <Snowflake size={16} className="text-sky-400" />
          <h3 className="text-sm font-semibold text-white">Cold Tables</h3>
        </div>
        <p className="text-xs text-zinc-500 mb-4">
          Not read by any job in this project &amp; region during the selected window — archival / deletion candidates
        </p>
        {coldTables.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-zinc-500 border-b border-zinc-800/50">
                  <th className="text-left py-2 px-3 font-medium">#</th>
                  <th className="text-left py-2 px-3 font-medium">Dataset</th>
                  <th className="text-left py-2 px-3 font-medium">Table</th>
                  <th className="text-left py-2 px-3 font-medium">Tier</th>
                  <th className="text-right py-2 px-3 font-medium">Size</th>
                  <th className="text-right py-2 px-3 font-medium">Est. $/mo if deleted</th>
                </tr>
              </thead>
              <tbody>
                {coldTables.map((t, i) => {
                  const rate = t.storage_tier === 'LONG_TERM' ? STORAGE_RATES.longTermLogical : STORAGE_RATES.activeLogical;
                  const saved = (t.total_bytes / Math.pow(1024, 3)) * rate;
                  return (
                    <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                      <td className="py-2.5 px-3 text-zinc-600 font-mono">{i + 1}</td>
                      <td className="py-2.5 px-3 text-zinc-400 font-mono">{t.dataset}</td>
                      <td className="py-2.5 px-3 text-white font-mono">{t.table_name}</td>
                      <td className="py-2.5 px-3">
                        <span className={`inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium border ${
                          t.storage_tier === 'LONG_TERM'
                            ? 'text-sky-400 bg-sky-500/5 border-sky-500/15'
                            : 'text-zinc-400 bg-zinc-500/5 border-zinc-500/15'
                        }`}>
                          {t.storage_tier}
                        </span>
                      </td>
                      <td className="py-2.5 px-3 text-right text-zinc-300 font-mono">{formatBytes(t.total_bytes)}</td>
                      <td className="py-2.5 px-3 text-right text-emerald-400 font-mono">${saved.toFixed(2)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState text="No cold tables — everything was referenced in this window" />
        )}
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

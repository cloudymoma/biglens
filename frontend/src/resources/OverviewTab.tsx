import { useEffect, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { fetchResourcesOverview } from '../api';
import type { ResOverviewData } from '../types';
import { EmptyState, ErrorBanner } from '../dashboards/shared';
import { Panel, CHART_TOOLTIP, AXIS_LABEL, th, td, type ResTabProps } from './shared';

export default function OverviewTab({ project, refreshKey }: ResTabProps) {
  const [data, setData] = useState<ResOverviewData | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setData(null);
    setError('');
    fetchResourcesOverview(project, refreshKey > 0)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message));
  }, [project, refreshKey]);

  if (error) return <ErrorBanner message={error} />;
  if (!data) return <EmptyState text="Loading inventory overview…" />;

  const kpis = [
    { label: 'Total resources', value: data.total_resources },
    { label: 'VMs running', value: data.vms_running },
    { label: 'VMs stopped', value: data.vms_stopped },
    { label: 'Buckets', value: data.buckets },
    { label: 'VPCs', value: data.vpcs },
    { label: 'Firewall rules', value: data.firewall_rules },
  ];

  const donutOption = {
    tooltip: { trigger: 'item', ...CHART_TOOLTIP },
    series: [{
      type: 'pie',
      radius: ['45%', '72%'],
      label: { color: '#a1a1aa', fontSize: 10 },
      data: data.by_service.slice(0, 12).map(s => ({ name: s.name, value: s.count })),
    }],
  };

  const barOption = {
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
    grid: { left: 90, right: 16, top: 8, bottom: 24 },
    xAxis: { type: 'value', axisLabel: AXIS_LABEL },
    yAxis: {
      type: 'category',
      axisLabel: AXIS_LABEL,
      data: data.by_location.slice(0, 10).map(l => l.name).reverse(),
    },
    series: [{ type: 'bar', itemStyle: { color: '#38bdf8' }, data: data.by_location.slice(0, 10).map(l => l.count).reverse() }],
  };

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 md:grid-cols-6 gap-3">
        {kpis.map(k => (
          <div key={k.label} className="bg-zinc-900/60 border border-zinc-800/60 rounded-xl p-3">
            <p className="text-[10px] uppercase tracking-wide text-zinc-500">{k.label}</p>
            <p className="text-xl font-semibold text-zinc-100 mt-1">{k.value.toLocaleString()}</p>
          </div>
        ))}
      </div>
      {data.truncated && (
        <p className="text-[11px] text-amber-300">
          Inventory truncated at 1000 resources — counts below reflect the first 1000 only.
        </p>
      )}
      <div className="grid md:grid-cols-2 gap-4">
        <Panel title="Resources by service">
          <ReactECharts style={{ height: 280 }} option={donutOption} />
        </Panel>
        <Panel title="Resources by location">
          <ReactECharts style={{ height: 280 }} option={barOption} />
        </Panel>
      </div>
      <Panel title="Recently created" note={`fetched ${data.fetched_at}`}>
        <table className="w-full text-sm">
          <thead><tr><th className={th}>Name</th><th className={th}>Type</th><th className={th}>Location</th><th className={th}>Created</th></tr></thead>
          <tbody>
            {data.recent.map(a => (
              <tr key={a.name} className="border-t border-zinc-800/40">
                <td className={td}>{a.display_name || a.name.split('/').pop()}</td>
                <td className={td}>{a.asset_type}</td>
                <td className={td}>{a.location}</td>
                <td className={td}>{a.created.slice(0, 10)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
    </div>
  );
}

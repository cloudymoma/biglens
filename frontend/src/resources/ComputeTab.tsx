import { useEffect, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { fetchResourcesCompute } from '../api';
import type { ResComputeData } from '../types';
import { EmptyState, ErrorBanner } from '../dashboards/shared';
import { Panel, CHART_TOOLTIP, AXIS_LABEL, th, td, WorkloadBadge, type ResTabProps } from './shared';

export default function ComputeTab({ project, refreshKey }: ResTabProps) {
  const [data, setData] = useState<ResComputeData | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setData(null);
    setError('');
    fetchResourcesCompute(project, refreshKey > 0)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message));
  }, [project, refreshKey]);

  if (error) return <ErrorBanner message={error} />;
  if (!data) return <EmptyState text="Loading compute inventory…" />;
  if (data.instances.length === 0 && data.disks.length === 0) {
    return <EmptyState text="No Compute Engine resources in this project." />;
  }

  const byWorkload = new Map<string, number>();
  const byFamily = new Map<string, number>();
  for (const vm of data.instances) {
    byWorkload.set(vm.workload, (byWorkload.get(vm.workload) ?? 0) + 1);
    const fam = vm.machine_type.split('-')[0];
    byFamily.set(fam, (byFamily.get(fam) ?? 0) + 1);
  }
  const workloadOption = {
    tooltip: { trigger: 'item', ...CHART_TOOLTIP },
    series: [{
      type: 'pie', radius: ['45%', '72%'], label: { color: '#a1a1aa', fontSize: 10 },
      data: [...byWorkload].map(([name, value]) => ({ name, value })),
    }],
  };
  const familyOption = {
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
    grid: { left: 60, right: 16, top: 8, bottom: 24 },
    xAxis: { type: 'category', axisLabel: AXIS_LABEL, data: [...byFamily.keys()] },
    yAxis: { type: 'value', axisLabel: AXIS_LABEL },
    series: [{ type: 'bar', itemStyle: { color: '#38bdf8' }, data: [...byFamily.values()] }],
  };

  return (
    <div className="space-y-4">
      <div className="grid md:grid-cols-2 gap-4">
        <Panel title="Instances by workload"><ReactECharts style={{ height: 240 }} option={workloadOption} /></Panel>
        <Panel title="Instances by machine family"><ReactECharts style={{ height: 240 }} option={familyOption} /></Panel>
      </div>
      <Panel title={`Instances (${data.instances.length})`} note={`fetched ${data.fetched_at}`}>
        <table className="w-full text-sm">
          <thead><tr><th className={th}>Name</th><th className={th}>Zone</th><th className={th}>Machine type</th><th className={th}>vCPU / RAM</th><th className={th}>Workload</th><th className={th}>Status</th></tr></thead>
          <tbody>
            {data.instances.map(vm => (
              <tr key={`${vm.zone}/${vm.name}`} className="border-t border-zinc-800/40">
                <td className={td}>{vm.name}</td>
                <td className={td}>{vm.zone}</td>
                <td className={td}>{vm.machine_type}</td>
                <td className={td}>{vm.vcpus > 0 ? `${vm.vcpus} / ${vm.memory_gb} GB` : '—'}</td>
                <td className={td}><WorkloadBadge workload={vm.workload} /></td>
                <td className={`${td} ${vm.status === 'RUNNING' ? 'text-emerald-300' : 'text-zinc-500'}`}>{vm.status}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
      <Panel title={`Disks (${data.disks.length})`}>
        <table className="w-full text-sm">
          <thead><tr><th className={th}>Name</th><th className={th}>Zone</th><th className={th}>Type</th><th className={th}>Size</th><th className={th}>Attached to</th></tr></thead>
          <tbody>
            {data.disks.map(d => (
              <tr key={`${d.zone}/${d.name}`} className="border-t border-zinc-800/40">
                <td className={td}>{d.name}</td>
                <td className={td}>{d.zone}</td>
                <td className={td}>{d.type}</td>
                <td className={td}>{d.size_gb} GB</td>
                <td className={td}>{d.users?.join(', ') || <span className="text-amber-300">unattached</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
    </div>
  );
}

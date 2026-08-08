import { useEffect, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { fetchResourcesStorage } from '../api';
import type { ResStorageData } from '../types';
import { EmptyState, ErrorBanner } from '../dashboards/shared';
import { Panel, CHART_TOOLTIP, AXIS_LABEL, th, td, fmtBytes, type ResTabProps } from './shared';

export default function StorageTab({ project, refreshKey }: ResTabProps) {
  const [data, setData] = useState<ResStorageData | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setData(null);
    setError('');
    fetchResourcesStorage(project, refreshKey > 0)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message));
  }, [project, refreshKey]);

  if (error) return <ErrorBanner message={error} />;
  if (!data) return <EmptyState text="Loading storage inventory…" />;
  if (data.buckets.length === 0) return <EmptyState text="No Cloud Storage buckets in this project." />;

  const classes = [...new Set(data.buckets.flatMap(b => Object.keys(b.bytes_by_class ?? {})))];
  const withBytes = data.buckets.filter(b => b.bytes_by_class && Object.keys(b.bytes_by_class).length > 0);
  const stackOption = {
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP, valueFormatter: (v: number) => fmtBytes(v) },
    legend: { textStyle: { color: '#a1a1aa', fontSize: 10 } },
    grid: { left: 90, right: 16, top: 28, bottom: 24 },
    xAxis: { type: 'value', axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtBytes(v) } },
    yAxis: { type: 'category', axisLabel: AXIS_LABEL, data: withBytes.map(b => b.name) },
    series: classes.map(cls => ({
      name: cls, type: 'bar', stack: 'bytes',
      data: withBytes.map(b => b.bytes_by_class?.[cls] ?? 0),
    })),
  };

  const bucketBytes = (b: (typeof data.buckets)[number]) =>
    Object.values(b.bytes_by_class ?? {}).reduce((a, v) => a + v, 0);

  return (
    <div className="space-y-4">
      {withBytes.length > 0 && (
        <Panel title="Bytes by storage class" note="daily Monitoring metric — may lag up to 24 h">
          <ReactECharts style={{ height: Math.max(220, withBytes.length * 28 + 80) }} option={stackOption} />
        </Panel>
      )}
      <Panel title={`Buckets (${data.buckets.length})`} note={`fetched ${data.fetched_at}`}>
        <table className="w-full text-sm">
          <thead><tr><th className={th}>Name</th><th className={th}>Location</th><th className={th}>Default class</th><th className={th}>Size</th><th className={th}>Uniform access</th><th className={th}>Public access</th><th className={th}>Created</th></tr></thead>
          <tbody>
            {data.buckets.map(b => (
              <tr key={b.name} className="border-t border-zinc-800/40">
                <td className={td}>{b.name}</td>
                <td className={td}>{b.location}</td>
                <td className={td}>{b.storage_class}</td>
                <td className={td}>{bucketBytes(b) > 0 ? fmtBytes(bucketBytes(b)) : '—'}</td>
                <td className={td}>{b.uniform_access ? 'yes' : <span className="text-amber-300">no (ACLs)</span>}</td>
                <td className={td}>{b.public_access_prevention}</td>
                <td className={td}>{b.created.slice(0, 10)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
    </div>
  );
}

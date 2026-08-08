import { useEffect, useState } from 'react';
import { fetchResourcesInsights } from '../api';
import type { ResFinding, ResInsightsData } from '../types';
import { EmptyState, ErrorBanner } from '../dashboards/shared';
import { Panel, td, th, type ResTabProps } from './shared';

const SEV_STYLE: Record<ResFinding['severity'], string> = {
  high: 'text-rose-300 border-rose-800/60 bg-rose-950/40',
  medium: 'text-amber-300 border-amber-800/60 bg-amber-950/40',
  low: 'text-zinc-300 border-zinc-700/60 bg-zinc-900/40',
};

export default function InsightsTab({ project, refreshKey }: ResTabProps) {
  const [data, setData] = useState<ResInsightsData | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setData(null);
    setError('');
    fetchResourcesInsights(project, refreshKey > 0)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message));
  }, [project, refreshKey]);

  if (error) return <ErrorBanner message={error} />;
  if (!data) return <EmptyState text="Analyzing inventory…" />;
  if (data.findings.length === 0) return <EmptyState text="No findings — inventory looks clean." />;

  return (
    <Panel title={`Findings (${data.findings.length})`} note={`fetched ${data.fetched_at}`}>
      <table className="w-full text-sm">
        <thead><tr><th className={th}>Severity</th><th className={th}>Category</th><th className={th}>Resource</th><th className={th}>Location</th><th className={th}>Why it matters</th></tr></thead>
        <tbody>
          {data.findings.map((f, i) => (
            <tr key={`${f.category}:${f.resource}:${i}`} className="border-t border-zinc-800/40">
              <td className={td}>
                <span className={`inline-block text-[10px] px-1.5 py-0.5 rounded border ${SEV_STYLE[f.severity]}`}>
                  {f.severity}
                </span>
              </td>
              <td className={td}>{f.category.replace(/_/g, ' ')}</td>
              <td className={`${td} font-mono text-xs`}>{f.resource}</td>
              <td className={td}>{f.location}</td>
              <td className={td}>{f.summary}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Panel>
  );
}

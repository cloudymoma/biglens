import { useEffect, useState } from 'react';
import { billingParams, fetchBillingProjects } from '../../api';
import type { BillingFilterState, BillingMeta, BillingProjectsData } from '../../types';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import { Panel, fmtMoney } from './shared';

interface TabProps {
  filter: BillingFilterState;
  meta: BillingMeta;
}

const th = 'text-left text-[11px] uppercase tracking-wide text-zinc-500 font-medium py-1.5 pr-4';
const td = 'py-1.5 pr-4 text-zinc-300';

export default function ProjectsTab({ filter, meta }: TabProps) {
  const [groupLabel, setGroupLabel] = useState('');
  const [data, setData] = useState<BillingProjectsData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const paramsKey = JSON.stringify(billingParams(filter)) + groupLabel;

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchBillingProjects(filter, groupLabel || undefined)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paramsKey]);

  const cur = meta.dataset.currency;
  if (error) return <ErrorBanner message={error} />;
  if (loading || !data) return <EmptyState text="Loading project spend…" />;

  return (
    <div className="space-y-4">
      <Panel title="Cost by project">
        {data.projects.length === 0 ? <EmptyState text="No cost in this window." /> : (
          <table className="w-full text-sm">
            <thead><tr><th className={th}>Project</th><th className={th}>Gross</th><th className={th}>Credits</th><th className={th}>Net</th></tr></thead>
            <tbody>
              {data.projects.map(p => (
                <tr key={p.id} className="border-t border-zinc-800/40">
                  <td className={td}>{p.name || p.id}<span className="ml-2 text-[10px] text-zinc-600">{p.id}</span></td>
                  <td className={td}>{fmtMoney(p.gross, cur)}</td>
                  <td className={td}>{fmtMoney(p.credits, cur)}</td>
                  <td className={`${td} font-medium text-zinc-100`}>{fmtMoney(p.net, cur)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>
      <Panel title="Group by label" note="one label key at a time — multi-key sums double-count">
        <div className="mb-3">
          <select
            value={groupLabel}
            onChange={e => setGroupLabel(e.target.value)}
            className="bg-zinc-900 border border-zinc-700 rounded-lg px-2 py-1.5 text-xs text-zinc-200"
          >
            <option value="">Choose a label key…</option>
            {meta.label_keys.map(k => (
              <option key={k} value={k}>{k}</option>
            ))}
          </select>
        </div>
        {groupLabel === '' ? (
          <EmptyState text="Pick a label key (e.g. env, team) to slice cost by its values." />
        ) : data.label_groups.length === 0 ? <EmptyState text="No rows for this label key." /> : (
          <table className="w-full text-sm">
            <thead><tr><th className={th}>{groupLabel}</th><th className={th}>Gross</th><th className={th}>Credits</th><th className={th}>Net</th></tr></thead>
            <tbody>
              {data.label_groups.map(g => (
                <tr key={g.name} className="border-t border-zinc-800/40">
                  <td className={td}>{g.name}</td>
                  <td className={td}>{fmtMoney(g.gross, cur)}</td>
                  <td className={td}>{fmtMoney(g.credits, cur)}</td>
                  <td className={`${td} font-medium text-zinc-100`}>{fmtMoney(g.net, cur)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>
    </div>
  );
}

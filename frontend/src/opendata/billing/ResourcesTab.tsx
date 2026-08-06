import { useEffect, useState } from 'react';
import { billingParams, fetchBillingResources } from '../../api';
import type { BillingFilterState, BillingMeta, BillingResourcesData } from '../../types';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import { MissingTableBanner, Panel, fmtMoney } from './shared';

interface TabProps {
  filter: BillingFilterState;
  meta: BillingMeta;
}

const th = 'text-left text-[11px] uppercase tracking-wide text-zinc-500 font-medium py-1.5 pr-4';
const td = 'py-1.5 pr-4 text-zinc-300';

export default function ResourcesTab({ filter, meta }: TabProps) {
  const [q, setQ] = useState('');
  const [applied, setApplied] = useState('');
  const [data, setData] = useState<BillingResourcesData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const paramsKey = JSON.stringify(billingParams(filter)) + applied;

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchBillingResources(filter, applied || undefined)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paramsKey]);

  if (error) return <ErrorBanner message={error} />;
  if (loading || !data) return <EmptyState text="Loading resource spend…" />;
  if (!data.available) {
    return (
      <MissingTableBanner
        table="gcp_billing_export_resource_v1_*"
        dataset={filter.dataset}
        docsUrl="https://cloud.google.com/billing/docs/how-to/export-data-bigquery-setup"
      />
    );
  }

  const cur = meta.dataset.currency;
  return (
    <Panel title="Top resources (net)" note="detailed resource-level export">
      <div className="mb-3 flex gap-2">
        <input
          value={q}
          onChange={e => setQ(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') setApplied(q.trim()); }}
          placeholder="search resource name or global name…"
          className="flex-1 bg-zinc-900 border border-zinc-700 rounded-lg px-3 py-1.5 text-xs text-zinc-200 placeholder-zinc-600"
        />
        <button onClick={() => setApplied(q.trim())} className="px-3 py-1.5 rounded-lg text-xs bg-zinc-800 text-zinc-300">
          Search
        </button>
      </div>
      {data.resources.length === 0 ? <EmptyState text="No resources match." /> : (
        <table className="w-full text-sm">
          <thead><tr><th className={th}>Resource</th><th className={th}>Service</th><th className={th}>Project</th><th className={th}>Net</th></tr></thead>
          <tbody>
            {data.resources.map((r0, i) => (
              <tr key={`${r0.global_name || r0.name}:${i}`} className="border-t border-zinc-800/40">
                <td className={`${td} break-all`}>{r0.name || r0.global_name || '(unnamed)'}</td>
                <td className={td}>{r0.service}</td>
                <td className={td}>{r0.project}</td>
                <td className={`${td} font-medium text-zinc-100`}>{fmtMoney(r0.net, cur)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

import { useEffect, useState } from 'react';
import { billingParams, fetchBillingResources } from '../api';
import type { BillingFilterState, BillingMeta, BillingResourceRow, BillingResourcesData } from '../types';
import { EmptyState, ErrorBanner } from '../dashboards/shared';
import { MissingTableBanner, Panel, fmtMoney } from './shared';

interface TabProps {
  filter: BillingFilterState;
  meta: BillingMeta;
}

const th = 'text-left text-[11px] uppercase tracking-wide text-zinc-500 font-medium py-1.5 pr-4';
const td = 'py-1.5 pr-4 text-zinc-300';

// Row label: the resource name, else the last segment of its global name
// (e.g. a Compute Engine instance ID); the full global name is the tooltip.
function resourceLabel(r: BillingResourceRow): string {
  return r.name || r.global_name.split('/').filter(Boolean).pop() || r.id;
}

// Net on export rows that carry no resource at all never shows up in the
// list; say how much of the window that is so the top 50 isn't read as all.
function unattributedNote(d: BillingResourcesData, cur: string): string {
  const un = d.unattributed_net;
  const total = d.total_net;
  if (Math.abs(un) < 0.005) return '';
  if (total > 0 && un > 0 && un <= total) {
    const pct = (un / total) * 100;
    return `${pct < 0.1 ? '<0.1' : pct.toFixed(1)}% of net cost in this window (${fmtMoney(un, cur)} of ${fmtMoney(total, cur)}) has no resource attribution (not shown).`;
  }
  return `${fmtMoney(un, cur)} of net cost in this window has no resource attribution (not shown); window total ${fmtMoney(total, cur)}.`;
}

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
  const note = unattributedNote(data, cur);
  return (
    <Panel title="Top resources (net)" note="one row per resource (global name) · detailed export">
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
      {note && <p className="mb-3 text-xs text-amber-300/80">{note}</p>}
      {data.resources.length === 0 ? <EmptyState text="No resources match." /> : (
        <table className="w-full text-sm">
          <thead><tr><th className={th}>Resource</th><th className={th}>Service</th><th className={th}>Project</th><th className={th}>Net</th></tr></thead>
          <tbody>
            {data.resources.map(r0 => (
              <tr key={r0.id} className="border-t border-zinc-800/40">
                <td className={`${td} break-all`} title={r0.global_name || r0.id}>{resourceLabel(r0)}</td>
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

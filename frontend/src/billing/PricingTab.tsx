import { useEffect, useState } from 'react';
import { fetchBillingPricing } from '../api';
import type { BillingFilterState, BillingMeta, BillingPricingData } from '../types';
import { EmptyState, ErrorBanner } from '../dashboards/shared';
import { MissingTableBanner, Panel } from './shared';

interface TabProps {
  filter: BillingFilterState;
  meta: BillingMeta;
}

const th = 'text-left text-[11px] uppercase tracking-wide text-zinc-500 font-medium py-1.5 pr-4';
const td = 'py-1.5 pr-4 text-zinc-300';

export default function PricingTab({ filter, meta }: TabProps) {
  const [q, setQ] = useState('');
  const [applied, setApplied] = useState('');
  const [data, setData] = useState<BillingPricingData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  // Pricing ignores the time window; only dataset/services/search matter.
  const paramsKey = filter.dataset + JSON.stringify(filter.services) + applied;

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchBillingPricing(filter, applied || undefined)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paramsKey]);

  if (error) return <ErrorBanner message={error} />;
  if (loading || !data) return <EmptyState text="Loading pricing…" />;
  if (!data.available) {
    return (
      <MissingTableBanner
        table="cloud_pricing_export"
        dataset={filter.dataset}
        docsUrl="https://cloud.google.com/billing/docs/how-to/export-data-bigquery-setup"
      />
    );
  }

  const cur = meta.dataset.currency;
  return (
    <Panel title="SKU pricing" note={`snapshot as of ${data.as_of || 'n/a'} · prices in ${cur} · first-tier rates`}>
      <div className="mb-3 flex gap-2">
        <input
          value={q}
          onChange={e => setQ(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') setApplied(q.trim()); }}
          placeholder="search SKU description…"
          className="flex-1 bg-zinc-900 border border-zinc-700 rounded-lg px-3 py-1.5 text-xs text-zinc-200 placeholder-zinc-600"
        />
        <button onClick={() => setApplied(q.trim())} className="px-3 py-1.5 rounded-lg text-xs bg-zinc-800 text-zinc-300">
          Search
        </button>
      </div>
      {data.prices.length === 0 ? <EmptyState text="No SKUs match." /> : (
        <table className="w-full text-sm">
          <thead>
            <tr>
              <th className={th}>SKU</th><th className={th}>Service</th><th className={th}>Unit</th>
              <th className={th}>List</th><th className={th}>Contract</th><th className={th}>Discount</th><th className={th}>Tiers</th>
            </tr>
          </thead>
          <tbody>
            {data.prices.map(p => (
              <tr key={p.sku_id} className="border-t border-zinc-800/40">
                <td className={td}>{p.sku}<span className="ml-2 text-[10px] text-zinc-600">{p.sku_id}</span></td>
                <td className={td}>{p.service}</td>
                <td className={td}>{p.pricing_unit}</td>
                <td className={td}>{p.list_price.toPrecision(4)}</td>
                <td className={td}>{p.contract_price === null ? '—' : p.contract_price.toPrecision(4)}</td>
                <td className={td}>{p.discount_pct === null ? '—' : `${p.discount_pct.toFixed(1)}%`}</td>
                <td className={td}>{p.tiers}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

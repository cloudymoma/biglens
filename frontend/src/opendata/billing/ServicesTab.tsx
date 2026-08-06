import { useEffect, useState } from 'react';
import { billingParams, fetchBillingServices } from '../../api';
import type { BillingFilterState, BillingMeta, BillingServicesData } from '../../types';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import { Panel, fmtMoney } from './shared';

interface TabProps {
  filter: BillingFilterState;
  meta: BillingMeta;
}

const th = 'text-left text-[11px] uppercase tracking-wide text-zinc-500 font-medium py-1.5 pr-4';
const td = 'py-1.5 pr-4 text-zinc-300';

export default function ServicesTab({ filter, meta }: TabProps) {
  const [service, setService] = useState('');
  const [data, setData] = useState<BillingServicesData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const paramsKey = JSON.stringify(billingParams(filter)) + service;

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchBillingServices(filter, service || undefined)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paramsKey]);

  const cur = meta.dataset.currency;
  if (error) return <ErrorBanner message={error} />;
  if (loading || !data) return <EmptyState text="Loading service breakdown…" />;

  return (
    <Panel
      title={service ? `SKUs — ${service}` : 'Cost by service'}
      note={service ? 'unit price = net / usage in pricing units' : 'net = gross + credits'}
    >
      <div className="mb-3">
        <select
          value={service}
          onChange={e => setService(e.target.value)}
          className="bg-zinc-900 border border-zinc-700 rounded-lg px-2 py-1.5 text-xs text-zinc-200"
        >
          <option value="">All services</option>
          {meta.services.map(s => (
            <option key={s} value={s}>{s}</option>
          ))}
        </select>
      </div>
      {service === '' ? (
        data.services.length === 0 ? <EmptyState text="No cost in this window." /> : (
          <table className="w-full text-sm">
            <thead><tr><th className={th}>Service</th><th className={th}>Gross</th><th className={th}>Credits</th><th className={th}>Net</th></tr></thead>
            <tbody>
              {data.services.map(s => (
                <tr key={s.name} className="border-t border-zinc-800/40 hover:bg-zinc-800/30 cursor-pointer" onClick={() => setService(s.name)}>
                  <td className={td}>{s.name}</td>
                  <td className={td}>{fmtMoney(s.gross, cur)}</td>
                  <td className={td}>{fmtMoney(s.credits, cur)}</td>
                  <td className={`${td} font-medium text-zinc-100`}>{fmtMoney(s.net, cur)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )
      ) : data.skus.length === 0 ? <EmptyState text="No SKUs for this service in the window." /> : (
        <table className="w-full text-sm">
          <thead><tr><th className={th}>SKU</th><th className={th}>Usage</th><th className={th}>Unit</th><th className={th}>Eff. price</th><th className={th}>Net</th></tr></thead>
          <tbody>
            {data.skus.map(s => (
              <tr key={s.sku_id} className="border-t border-zinc-800/40">
                <td className={td}>{s.sku}<span className="ml-2 text-[10px] text-zinc-600">{s.sku_id}</span></td>
                <td className={td}>{s.usage.toLocaleString()}</td>
                <td className={td}>{s.pricing_unit}</td>
                <td className={td}>{s.effective_price === null ? '—' : s.effective_price.toPrecision(4)}</td>
                <td className={`${td} font-medium text-zinc-100`}>{fmtMoney(s.net, cur)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

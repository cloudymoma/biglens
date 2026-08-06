import { useEffect, useState } from 'react';
import { billingParams, fetchBillingCredits } from '../api';
import type { BillingFilterState, BillingMeta, BillingCreditsData } from '../types';
import { EmptyState, ErrorBanner } from '../dashboards/shared';
import { Panel, fmtMoney } from './shared';

interface TabProps {
  filter: BillingFilterState;
  meta: BillingMeta;
}

const th = 'text-left text-[11px] uppercase tracking-wide text-zinc-500 font-medium py-1.5 pr-4';
const td = 'py-1.5 pr-4 text-zinc-300';

export default function CreditsTab({ filter, meta }: TabProps) {
  const [data, setData] = useState<BillingCreditsData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const paramsKey = JSON.stringify(billingParams(filter));

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchBillingCredits(filter)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paramsKey]);

  const cur = meta.dataset.currency;
  if (error) return <ErrorBanner message={error} />;
  if (loading || !data) return <EmptyState text="Loading credits…" />;

  const discounted = data.by_service.filter(s => s.credits !== 0);

  return (
    <div className="space-y-4">
      <Panel title="Credits by type" note="CUDs, SUDs, free tier, promotions — amounts are negative">
        {data.credits.length === 0 ? <EmptyState text="No credits in this window." /> : (
          <table className="w-full text-sm">
            <thead><tr><th className={th}>Type</th><th className={th}>Credit</th><th className={th}>Amount</th></tr></thead>
            <tbody>
              {data.credits.map((c, i) => (
                <tr key={`${c.type}:${c.name}:${i}`} className="border-t border-zinc-800/40">
                  <td className={td}>{c.type}</td>
                  <td className={td}>{c.name}</td>
                  <td className={`${td} font-medium text-emerald-400`}>{fmtMoney(c.amount, cur)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>
      <Panel title="Effective discount by service" note="discount % = −credits ÷ gross">
        {discounted.length === 0 ? <EmptyState text="No service received credits in this window." /> : (
          <table className="w-full text-sm">
            <thead><tr><th className={th}>Service</th><th className={th}>Gross</th><th className={th}>Credits</th><th className={th}>Net</th><th className={th}>Discount</th></tr></thead>
            <tbody>
              {discounted.map(s => (
                <tr key={s.name} className="border-t border-zinc-800/40">
                  <td className={td}>{s.name}</td>
                  <td className={td}>{fmtMoney(s.gross, cur)}</td>
                  <td className={`${td} text-emerald-400`}>{fmtMoney(s.credits, cur)}</td>
                  <td className={td}>{fmtMoney(s.net, cur)}</td>
                  <td className={`${td} font-medium text-zinc-100`}>
                    {s.gross > 0 ? `${((-s.credits / s.gross) * 100).toFixed(1)}%` : '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>
    </div>
  );
}

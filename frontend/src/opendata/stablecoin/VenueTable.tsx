import { fmtBps, fmtMoney, fmtPrice } from './format';
import { cx } from './styles';
import type { StableProviderHealth, StableQuote } from './types';
import { ColdState, CsvButton, Panel } from './ui';

const KIND_HELP: Record<string, string> = {
  direct: 'quoted in the asset’s numéraire on the venue',
  derived: 'two legs on the same venue (num ÷ den); weighted by the smaller leg; excluded when a leg is',
  cross: 'legs from different venues (cross-venue); flagged as partly dependent',
  inverse: '1 ÷ the venue’s quote',
  dex: 'Uniswap v3 pool price over RPC',
  market: 'a raw venue market, evaluated on its own',
};

// devOf is the input's deviation from par in bps, when its quote is in the
// asset's numéraire (raw markets in other currencies have none).
function devOf(q: StableQuote, numeraire: string): number | null {
  if (q.mid === null || q.numeraire !== numeraire) return null;
  return (q.mid - 1) * 1e4;
}

function statusOf(providers: StableProviderHealth[], venue: string): string | null {
  const p = providers.find(x => x.key === venue);
  return p ? (p.enabled ? p.status : 'disabled') : null;
}

// VenueTable (stablecoin.md §7.2): every input with its filters' outcome.
export default function VenueTable({
  title,
  quotes,
  numeraire,
  providers,
  filename,
  help,
}: {
  title: string;
  quotes: StableQuote[];
  numeraire: string;
  providers: StableProviderHealth[];
  filename: string;
  help?: string;
}) {
  return (
    <Panel
      title={title}
      help={
        help ??
        'Each input passes the §5.2 filters (age ≤ 5 min from the fetch, spread, 24h volume, response checks). The consensus is the volume-weighted median of included inputs with no venue above 50% of the weight; it needs at least 2 distinct venues, and with exactly 2 it is their mean.'
      }
      right={
        <CsvButton
          filename={filename}
          label={title}
          headers={['venue', 'pair', 'kind', 'numeraire', 'bid', 'ask', 'mid', 'spread_bps', 'vol_usd', 'dev_bps', 'weight', 'included', 'exclude_reason', 'age_seconds', 'last_trade_age_s']}
          rows={() =>
            quotes.map(q => [
              q.venue,
              q.pair,
              q.kind,
              q.numeraire,
              q.bid,
              q.ask,
              q.mid,
              q.spread_bps,
              q.vol_usd,
              devOf(q, numeraire),
              q.weight,
              q.included ? 'yes' : 'no',
              q.exclude_reason ?? '',
              q.age_seconds,
              q.last_trade_age_s ?? null,
            ])
          }
        />
      }
    >
      {quotes.length === 0 ? (
        <ColdState>Waiting for first sample (≤ 5 min).</ColdState>
      ) : (
        <div className="overflow-x-auto">
          <table className={cx.table}>
            <thead>
              <tr>
                {['Venue', 'Pair', 'Kind', 'Bid', 'Ask', 'Mid', 'Spread', '24h volume', 'Deviation', 'Weight', 'Status', 'Age'].map(h => (
                  <th key={h} scope="col" className={cx.th}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {quotes.map((q, i) => {
                const st = statusOf(providers, q.venue);
                return (
                  <tr key={`${q.venue}-${q.pair}-${i}`} className={cx.tr}>
                    <td className={cx.td}>
                      <span className={`font-medium ${cx.strong}`}>{q.venue}</span>
                      {st && st !== 'ok' && <span className={`ml-1 text-[10px] ${cx.muted}`}>· {st}</span>}
                    </td>
                    <td className={`${cx.td} ${cx.mono}`} title={q.legs?.join(' , ')}>
                      {q.pair}
                    </td>
                    <td className={cx.td} title={KIND_HELP[q.kind] ?? q.kind}>
                      {q.kind}
                    </td>
                    <td className={`${cx.td} font-mono`}>{fmtPrice(q.bid, q.numeraire)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtPrice(q.ask, q.numeraire)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtPrice(q.mid, q.numeraire)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtBps(q.spread_bps, 1, false)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtMoney(q.vol_usd, 'USD', 0)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtBps(devOf(q, numeraire))}</td>
                    <td className={`${cx.td} font-mono`}>{q.weight === null ? '—' : `${(q.weight * 100).toFixed(1)}%`}</td>
                    <td className={cx.td}>
                      {q.included ? (
                        <span className="text-emerald-700 dark:text-emerald-400">included</span>
                      ) : (
                        <span className={cx.muted}>excluded: {q.exclude_reason || 'unknown'}</span>
                      )}
                    </td>
                    <td className={`${cx.td} font-mono`} title={q.last_trade_age_s != null ? `last trade ${q.last_trade_age_s}s ago` : undefined}>
                      {q.age_seconds === null ? '—' : `${q.age_seconds}s`}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}

import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { ExternalLink } from 'lucide-react';
import type { AddressRiskChain, CryptoWhalesData, CryptoChain } from '../../types';
import { fetchCryptoWhales } from '../../api';
import { RISK_BADGES } from './addressRiskTools';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import {
  BTC_COLOR, ETH_COLOR, CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE,
  fmtNum, shortHash, Panel, DaysPicker,
} from './shared';

const DAY_OPTIONS = [7, 30, 90];

// Hashes are validated server-side against the chain's canonical shape
// before they reach this component; the URL is a fixed template around them.
const explorerUrl = (chain: CryptoChain, hash: string) =>
  chain === 'btc' ? `https://mempool.space/tx/${hash}` : `https://etherscan.io/tx/${hash}`;

const unit = (chain: CryptoChain) => (chain === 'btc' ? 'BTC' : 'ETH');

// An ETH address as a button that opens it in Address Risk, with badges for
// the local lists it is on (spec D17). Empty addresses (contract creations)
// render as a dash.
function EthAddress({ address, risk, onInspect }: {
  address: string;
  risk?: string[];
  onInspect?: (address: string, chain?: AddressRiskChain) => void;
}) {
  if (!address) return <span>—</span>;
  return (
    <span className="inline-flex items-center gap-1">
      <button type="button" title={`${address} — check in Address Risk`} onClick={() => onInspect?.(address, 'eth')}
              className="hover:text-white underline decoration-zinc-700">
        {shortHash(address)}
      </button>
      {(risk ?? []).map(r => (
        <span key={r} className="rounded px-1 text-[10px] font-sans font-semibold border"
              style={{ color: RISK_BADGES[r]?.color, borderColor: RISK_BADGES[r]?.color }}>
          {RISK_BADGES[r]?.label ?? r}
        </span>
      ))}
    </span>
  );
}

export default function WhalesTab({ onInspect }: { onInspect?: (address: string, chain?: AddressRiskChain) => void }) {
  const [days, setDays] = useState(90);
  const [chain, setChain] = useState<CryptoChain>('btc');
  const [data, setData] = useState<CryptoWhalesData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    let stale = false;
    setLoading(true);
    setError('');
    fetchCryptoWhales(days, chain)
      .then(d => { if (!stale) setData(d); })
      .catch(e => { if (!stale) setError(e.response?.data || e.message); })
      .finally(() => { if (!stale) setLoading(false); });
    return () => { stale = true; };
  }, [days, chain]);

  const controls = (
    <div className="flex items-center justify-between">
      <div className="flex items-center gap-3">
        <div className="flex items-center gap-1">
          {(['btc', 'eth'] as const).map(c => (
            <button
              key={c}
              onClick={() => setChain(c)}
              className={`px-3 py-1.5 rounded-lg text-xs font-semibold uppercase transition-colors ${
                chain === c ? 'text-white' : 'text-zinc-500 hover:text-zinc-300'
              }`}
              style={chain === c ? { background: `${c === 'btc' ? BTC_COLOR : ETH_COLOR}30` } : undefined}
            >
              {c}
            </button>
          ))}
        </div>
        <DaysPicker options={DAY_OPTIONS} value={days} onChange={setDays} />
      </div>
      <span className="text-[11px] text-zinc-600">native units — no USD rate exists on-chain</span>
    </div>
  );

  if (error) return <div className="space-y-4">{controls}<ErrorBanner message={error} /></div>;
  if (loading || !data || data.chain !== chain || data.days !== days) {
    return <div className="space-y-4">{controls}<EmptyState text="Loading whale activity…" /></div>;
  }

  const renderChain = data.chain;
  const color = renderChain === 'btc' ? BTC_COLOR : ETH_COLOR;
  const freezeNote = renderChain === 'eth' && data.freeze_coverage_from
    ? ` · Frozen badges cover since ${data.freeze_coverage_from}`
    : '';

  return (
    <div className="space-y-4">
      {controls}

      <div className="grid lg:grid-cols-2 gap-4">
        <Panel
          title={`Whale Activity (≥ ${fmtNum(data.threshold)} ${unit(renderChain)} per tx)`}
          note={renderChain === 'btc' ? 'tx total output incl. change — upper bound' : 'succeeded top-level txs (excl. internal transfers)'}
        >
          <ReactECharts
            style={{ height: 240 }}
            option={{
              tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
              grid: { left: 50, right: 20, top: 16, bottom: 24 },
              xAxis: { type: 'category', data: data.trend.map(r => r.date), axisLabel: AXIS_LABEL },
              yAxis: { type: 'value', axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE },
              series: [{ name: 'whale txs', type: 'bar', data: data.trend.map(r => r.whale_count), itemStyle: { color, opacity: 0.85 } }],
            }}
          />
        </Panel>
        <Panel title="Value Concentration" note="share of each day's moved value carried by the top 1% of value-bearing txs">
          <ReactECharts
            style={{ height: 240 }}
            option={{
              tooltip: { trigger: 'axis', ...CHART_TOOLTIP, valueFormatter: (v: number) => `${v}%` },
              grid: { left: 50, right: 20, top: 16, bottom: 24 },
              xAxis: { type: 'category', data: data.concentration.map(r => r.date), axisLabel: AXIS_LABEL },
              yAxis: { type: 'value', max: 100, axisLabel: { ...AXIS_LABEL, formatter: '{value}%' }, splitLine: SPLIT_LINE },
              series: [{ name: 'top 1% share', type: 'line', showSymbol: false, areaStyle: { color, opacity: 0.15 }, data: data.concentration.map(r => r.top1pct_share), lineStyle: { color, width: 2 }, itemStyle: { color } }],
            }}
          />
        </Panel>
      </div>

      <div className="grid lg:grid-cols-2 gap-4">
        <Panel
          title="Largest Transfers"
          note={renderChain === 'btc' ? 'total output value (incl. change); BTC has no single sender/receiver' : `succeeded top-level txs (excl. internal transfers) · sender → receiver${freezeNote}`}
        >
          {data.largest.length === 0 ? <EmptyState text="No transfers in range." /> : (
            <div className="overflow-y-auto max-h-96">
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-zinc-500 text-left">
                    <th className="pb-2 font-medium">Time (UTC)</th>
                    <th className="pb-2 font-medium">Transaction</th>
                    {renderChain === 'eth' && <th className="pb-2 font-medium">From → To</th>}
                    <th className="pb-2 font-medium text-right">{unit(renderChain)}</th>
                  </tr>
                </thead>
                <tbody className="font-mono">
                  {data.largest.map(tx => (
                    <tr key={tx.hash} className="border-t border-zinc-800/40 text-zinc-300">
                      <td className="py-1.5 text-zinc-500">{tx.time}</td>
                      <td className="py-1.5">
                        <a
                          href={explorerUrl(renderChain, tx.hash)}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="inline-flex items-center gap-1 hover:text-white"
                          style={{ color }}
                        >
                          {shortHash(tx.hash)} <ExternalLink size={11} />
                        </a>
                      </td>
                      {renderChain === 'eth' && (
                        <td className="py-1.5 text-zinc-500">
                          <EthAddress address={tx.from} risk={tx.from_risk} onInspect={onInspect} /> →{' '}
                          <EthAddress address={tx.to} risk={tx.to_risk} onInspect={onInspect} />
                        </td>
                      )}
                      <td className="py-1.5 text-right font-semibold text-white">{fmtNum(tx.amount)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
        <Panel
          title="Top Receiving Addresses"
          note={renderChain === 'btc' ? 'total output value received (excl. coinbase, incl. change)' : `total value received in succeeded top-level txs${freezeNote}`}
        >
          {data.top_receivers.length === 0 ? <EmptyState text="No receivers in range." /> : (
            <div className="overflow-y-auto max-h-96">
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-zinc-500 text-left">
                    <th className="pb-2 font-medium">Address</th>
                    <th className="pb-2 font-medium text-right">{renderChain === 'btc' ? 'Outputs' : 'Txs'}</th>
                    <th className="pb-2 font-medium text-right">{unit(renderChain)} received</th>
                  </tr>
                </thead>
                <tbody className="font-mono">
                  {data.top_receivers.map(a => (
                    <tr key={a.address} className="border-t border-zinc-800/40 text-zinc-300">
                      <td className="py-1.5">
                        {renderChain === 'eth' ? <EthAddress address={a.address} risk={a.risk} onInspect={onInspect} /> : shortHash(a.address)}
                      </td>
                      <td className="py-1.5 text-right text-zinc-500">{fmtNum(a.tx_count)}</td>
                      <td className="py-1.5 text-right font-semibold text-white">{fmtNum(a.total)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
      </div>
    </div>
  );
}


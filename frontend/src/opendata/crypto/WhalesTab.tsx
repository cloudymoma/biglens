import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { ExternalLink } from 'lucide-react';
import type { CryptoWhalesData, CryptoChain } from '../../types';
import { fetchCryptoWhales } from '../../api';
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

export default function WhalesTab() {
  const [days, setDays] = useState(90);
  const [chain, setChain] = useState<CryptoChain>('btc');
  const [data, setData] = useState<CryptoWhalesData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchCryptoWhales(days, chain)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [days, chain]);

  const color = chain === 'btc' ? BTC_COLOR : ETH_COLOR;
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
  if (loading || !data) return <div className="space-y-4">{controls}<EmptyState text="Loading whale activity…" /></div>;

  return (
    <div className="space-y-4">
      {controls}

      <div className="grid lg:grid-cols-2 gap-4">
        <Panel title={`Whale Activity (≥ ${fmtNum(data.threshold)} ${unit(chain)} per tx)`}>
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
        <Panel title="Value Concentration" note="share of each day's moved value carried by the top 1% largest txs">
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
          note={chain === 'btc' ? 'total output value; BTC has no single sender/receiver' : 'sender → receiver'}
        >
          {data.largest.length === 0 ? <EmptyState text="No transfers in range." /> : (
            <div className="overflow-y-auto max-h-96">
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-zinc-500 text-left">
                    <th className="pb-2 font-medium">Time (UTC)</th>
                    <th className="pb-2 font-medium">Transaction</th>
                    {chain === 'eth' && <th className="pb-2 font-medium">From → To</th>}
                    <th className="pb-2 font-medium text-right">{unit(chain)}</th>
                  </tr>
                </thead>
                <tbody className="font-mono">
                  {data.largest.map(tx => (
                    <tr key={tx.hash} className="border-t border-zinc-800/40 text-zinc-300">
                      <td className="py-1.5 text-zinc-500">{tx.time}</td>
                      <td className="py-1.5">
                        <a
                          href={explorerUrl(chain, tx.hash)}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="inline-flex items-center gap-1 hover:text-white"
                          style={{ color }}
                        >
                          {shortHash(tx.hash)} <ExternalLink size={11} />
                        </a>
                      </td>
                      {chain === 'eth' && (
                        <td className="py-1.5 text-zinc-500">{shortHash(tx.from)} → {shortHash(tx.to)}</td>
                      )}
                      <td className="py-1.5 text-right font-semibold text-white">{fmtNum(tx.amount)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
        <Panel title="Top Receiving Addresses" note="total value received in the window">
          {data.top_receivers.length === 0 ? <EmptyState text="No receivers in range." /> : (
            <div className="overflow-y-auto max-h-96">
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-zinc-500 text-left">
                    <th className="pb-2 font-medium">Address</th>
                    <th className="pb-2 font-medium text-right">Outputs</th>
                    <th className="pb-2 font-medium text-right">{unit(chain)} received</th>
                  </tr>
                </thead>
                <tbody className="font-mono">
                  {data.top_receivers.map(a => (
                    <tr key={a.address} className="border-t border-zinc-800/40 text-zinc-300">
                      <td className="py-1.5">{shortHash(a.address)}</td>
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

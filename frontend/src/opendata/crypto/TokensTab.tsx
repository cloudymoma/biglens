import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import type { CryptoTokensData, TokenRow } from '../../types';
import { fetchCryptoTokens } from '../../api';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import {
  ETH_COLOR, CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE,
  fmtNum, shortHash, Panel, DaysPicker,
} from './shared';

const DAY_OPTIONS = [7, 30];

const tokenLabel = (t: TokenRow) => t.symbol || t.name || shortHash(t.token_address);

export default function TokensTab() {
  const [days, setDays] = useState(30);
  const [data, setData] = useState<CryptoTokensData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchCryptoTokens(days)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [days]);

  if (error) return <div className="space-y-4"><DaysPicker options={DAY_OPTIONS} value={days} onChange={setDays} /><ErrorBanner message={error} /></div>;
  if (loading || !data) return <EmptyState text="Loading token economy…" />;

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <DaysPicker options={DAY_OPTIONS} value={days} onChange={setDays} />
        <span className="text-[11px] text-zinc-600">
          activity measured in transfer counts — cross-token value sums are meaningless without prices
        </span>
      </div>

      <div className="grid lg:grid-cols-2 gap-4">
        <Panel title="Token vs Native Activity" note="daily ERC-20 transfer events vs plain ETH transactions">
          <ReactECharts
            style={{ height: 260 }}
            option={{
              tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
              legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
              grid: { left: 60, right: 20, top: 32, bottom: 24 },
              xAxis: { type: 'category', data: data.daily.map(r => r.date), axisLabel: AXIS_LABEL },
              yAxis: { type: 'value', axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtNum(v) }, splitLine: SPLIT_LINE },
              series: [
                { name: 'ERC-20 transfers', type: 'line', showSymbol: false, areaStyle: { color: '#a855f7', opacity: 0.12 }, data: data.daily.map(r => r.transfers), lineStyle: { color: '#a855f7', width: 2 }, itemStyle: { color: '#a855f7' } },
                { name: 'ETH transactions', type: 'line', showSymbol: false, data: data.daily.map(r => r.native_txs), lineStyle: { color: ETH_COLOR, width: 2 }, itemStyle: { color: ETH_COLOR } },
              ],
            }}
          />
        </Panel>
        <Panel title="New Contracts Deployed" note="per day; ERC-20 / ERC-721 flagged by bytecode signature">
          <ReactECharts
            style={{ height: 260 }}
            option={{
              tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
              legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
              grid: { left: 60, right: 20, top: 32, bottom: 24 },
              xAxis: { type: 'category', data: data.contracts.map(r => r.date), axisLabel: AXIS_LABEL },
              yAxis: { type: 'value', axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtNum(v) }, splitLine: SPLIT_LINE },
              series: [
                { name: 'All contracts', type: 'bar', data: data.contracts.map(r => r.contracts), itemStyle: { color: '#3f3f46' } },
                { name: 'ERC-20', type: 'bar', data: data.contracts.map(r => r.erc20), itemStyle: { color: '#a855f7' } },
                { name: 'ERC-721', type: 'bar', data: data.contracts.map(r => r.erc721), itemStyle: { color: '#22c55e' } },
              ],
            }}
          />
        </Panel>
      </div>

      <div className="grid lg:grid-cols-2 gap-4">
        <Panel title="Token Movement" note="top 25 ERC-20 tokens sized by transfer count">
          <ReactECharts
            style={{ height: 340 }}
            option={{
              tooltip: {
                ...CHART_TOOLTIP,
                formatter: (p: { value: number }) => `${fmtNum(p.value)} transfers`,
              },
              series: [{
                type: 'treemap',
                roam: false,
                nodeClick: false,
                breadcrumb: { show: false },
                label: { color: '#e4e4e7', fontSize: 11, fontFamily: 'JetBrains Mono, monospace' },
                itemStyle: { borderColor: '#111114', borderWidth: 2, gapWidth: 2 },
                data: data.top_tokens.map((t, i) => ({
                  name: tokenLabel(t),
                  value: t.transfers,
                  itemStyle: { color: ['#a855f7', '#627eea', '#22c55e', '#fbbf24', '#ef4444'][i % 5], opacity: 0.55 + 0.45 * (1 - i / 25) },
                })),
              }],
            }}
          />
        </Panel>
        <Panel title="Top Tokens by Activity">
          {data.top_tokens.length === 0 ? <EmptyState text="No token transfers in range." /> : (
            <div className="overflow-y-auto max-h-[340px]">
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-zinc-500 text-left">
                    <th className="pb-2 font-medium">#</th>
                    <th className="pb-2 font-medium">Token</th>
                    <th className="pb-2 font-medium text-right">Transfers</th>
                    <th className="pb-2 font-medium text-right">Senders</th>
                    <th className="pb-2 font-medium text-right">Receivers</th>
                  </tr>
                </thead>
                <tbody className="font-mono">
                  {data.top_tokens.map((t, i) => (
                    <tr key={t.token_address} className="border-t border-zinc-800/40 text-zinc-300">
                      <td className="py-1.5 text-zinc-600">{i + 1}</td>
                      <td className="py-1.5">
                        <span className="text-white font-semibold">{tokenLabel(t)}</span>
                        <span className="text-zinc-600 ml-2">{shortHash(t.token_address)}</span>
                      </td>
                      <td className="py-1.5 text-right font-semibold text-white">{fmtNum(t.transfers)}</td>
                      <td className="py-1.5 text-right text-zinc-500">{fmtNum(t.senders)}</td>
                      <td className="py-1.5 text-right text-zinc-500">{fmtNum(t.receivers)}</td>
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

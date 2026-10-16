import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import type { AddressRiskChain, CryptoTokensData, TokenRow } from '../../types';
import { fetchCryptoTokens } from '../../api';
import { RISK_BADGES } from './addressRiskTools';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import TokensLivePanel from './TokensLivePanel';
import {
  BTC_COLOR, ETH_COLOR, CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE,
  fmtNum, shortHash, Panel, DaysPicker, IngestingNote,
} from './shared';

const DAY_OPTIONS = [7, 30];

const tokenLabel = (t: TokenRow) => t.symbol || t.name || shortHash(t.token_address);

function RiskBadges({ codes }: { codes?: string[] }) {
  if (!codes || codes.length === 0) return null;
  return (
    <>
      {codes.map(r => (
        <span
          key={r}
          className="rounded px-1 text-[10px] font-sans font-semibold border ml-1"
          style={{ color: RISK_BADGES[r]?.color ?? '#fb923c', borderColor: RISK_BADGES[r]?.color ?? '#fb923c' }}
        >
          {RISK_BADGES[r]?.label ?? r}
        </span>
      ))}
    </>
  );
}

export default function TokensTab({
  onInspect,
}: {
  onInspect?: (address: string, chain?: AddressRiskChain) => void;
}) {
  const [days, setDays] = useState(30);
  const [treemapMetric, setTreemapMetric] = useState<'transfers' | 'senders'>('transfers');
  const [data, setData] = useState<CryptoTokensData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    let stale = false;
    setLoading(true);
    setError('');
    fetchCryptoTokens(days)
      .then(d => { if (!stale) setData(d); })
      .catch(e => { if (!stale) setError(e.response?.data || e.message); })
      .finally(() => { if (!stale) setLoading(false); });
    return () => { stale = true; };
  }, [days]);

  if (error) {
    return (
      <div className="space-y-4">
        <DaysPicker options={DAY_OPTIONS} value={days} onChange={setDays} />
        <ErrorBanner message={error} />
      </div>
    );
  }
  if (loading || !data || data.days !== days) return <EmptyState text="Loading token economy…" />;

  const totalStableUSD = data.daily.reduce((s, r) => s + (r.stablecoin_usd ?? 0), 0);
  const totalWrappedUSD = data.daily.reduce((s, r) => s + (r.eth_token_usd ?? 0) + (r.btc_token_usd ?? 0), 0);
  const totalTransfers = data.daily.reduce((s, r) => s + r.transfers, 0);
  const totalZeroTransfers = data.daily.reduce((s, r) => s + (r.zero_value_transfers ?? 0), 0);
  const zeroSharePct = totalTransfers > 0 ? (totalZeroTransfers / totalTransfers) * 100 : 0;

  const netStableIssuedUSD = (data.major_tokens ?? [])
    .filter(m => m.category === 'stablecoin' && m.mint_burn_supported)
    .reduce((s, m) => s + m.net_issued_usd, 0);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <DaysPicker options={DAY_OPTIONS} value={days} onChange={setDays} />
        <span className="text-[11px] text-zinc-500">
          Settled days cached in local SQLite · USD volume priced via on-chain Uniswap v3 medians + live RPC supply
        </span>
      </div>
      <IngestingNote complete={data.last_day_complete} probeFailed={data.last_day_probe_failed} />

      {data.warnings && data.warnings.length > 0 && (
        <div className="rounded-xl border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-amber-300">
          Partial data warning: {data.warnings.join(' · ')}
        </div>
      )}

      {/* KPI Summary Strip */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <div className="rounded-2xl border border-zinc-800/50 p-4" style={{ background: '#111114' }}>
          <div className="text-[11px] text-zinc-500">Stablecoin On-Chain Volume ({days}d)</div>
          <div className="text-lg font-bold text-white font-mono mt-1">${fmtNum(totalStableUSD)}</div>
          <div className="text-[11px] text-zinc-600 mt-0.5">USDT, USDC, DAI, USDS, USDe, PYUSD, FDUSD</div>
        </div>
        <div className="rounded-2xl border border-zinc-800/50 p-4" style={{ background: '#111114' }}>
          <div className="text-[11px] text-zinc-500">Wrapped ETH &amp; BTC Volume ({days}d)</div>
          <div className="text-lg font-bold text-white font-mono mt-1">${fmtNum(totalWrappedUSD)}</div>
          <div className="text-[11px] text-zinc-600 mt-0.5">WETH, stETH, wstETH, WBTC, cbBTC</div>
        </div>
        <div className="rounded-2xl border border-zinc-800/50 p-4" style={{ background: '#111114' }}>
          <div className="text-[11px] text-zinc-500">Net Stablecoin Issuance ({days}d)</div>
          <div className={`text-lg font-bold font-mono mt-1 ${netStableIssuedUSD >= 0 ? 'text-emerald-400' : 'text-rose-400'}`}>
            {netStableIssuedUSD >= 0 ? '+' : '-'}${fmtNum(Math.abs(netStableIssuedUSD))}
          </div>
          <div className="text-[11px] text-zinc-600 mt-0.5">USDC, DAI, USDS, USDe, PYUSD, FDUSD (0x0 mint/burn)</div>
        </div>
        <div className="rounded-2xl border border-zinc-800/50 p-4" style={{ background: '#111114' }}>
          <div className="text-[11px] text-zinc-500">Zero-Value Transfer Share ({days}d)</div>
          <div className="text-lg font-bold text-amber-400 font-mono mt-1">{zeroSharePct.toFixed(1)}%</div>
          <div className="text-[11px] text-zinc-600 mt-0.5">
            {fmtNum(totalZeroTransfers)} of {fmtNum(totalTransfers)} transfers (incl. ERC-721 #0 &amp; poisoning)
          </div>
        </div>
      </div>

      {/* Live Cross-Chain Supply & Market Pulse */}
      <TokensLivePanel onInspect={onInspect} />

      {/* Major Token USD Volume & Mint/Burn Summary */}
      <div className="grid lg:grid-cols-2 gap-4">
        <Panel
          title="Major Token On-Chain USD Volume"
          note="daily USD transfer volume across 12 allowlisted stablecoins &amp; wrapped ETH/BTC tokens"
        >
          <ReactECharts
            style={{ height: 260 }}
            option={{
              tooltip: {
                trigger: 'axis',
                ...CHART_TOOLTIP,
                valueFormatter: (v: number) => `$${fmtNum(v)}`,
              },
              legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
              grid: { left: 64, right: 20, top: 32, bottom: 24 },
              xAxis: { type: 'category', data: data.daily.map(r => r.date), axisLabel: AXIS_LABEL },
              yAxis: {
                type: 'value',
                axisLabel: { ...AXIS_LABEL, formatter: (v: number) => `$${fmtNum(v)}` },
                splitLine: SPLIT_LINE,
              },
              series: [
                {
                  name: 'Stablecoins (USD)',
                  type: 'bar',
                  stack: 'usd',
                  data: data.daily.map(r => r.stablecoin_usd ?? 0),
                  itemStyle: { color: '#22c55e' },
                },
                {
                  name: 'ETH-pegged (WETH/stETH/wstETH)',
                  type: 'bar',
                  stack: 'usd',
                  data: data.daily.map(r => r.eth_token_usd ?? 0),
                  itemStyle: { color: ETH_COLOR },
                },
                {
                  name: 'BTC-pegged (WBTC/cbBTC)',
                  type: 'bar',
                  stack: 'usd',
                  data: data.daily.map(r => r.btc_token_usd ?? 0),
                  itemStyle: { color: BTC_COLOR },
                },
              ],
            }}
          />
        </Panel>

        <Panel
          title="Allowlisted Token Volume &amp; Mint / Burn"
          note="0x0 Transfer mint/burn; USDT (Issue/Redeem) &amp; WETH (Deposit/Withdrawal) shown as n/a"
        >
          <div className="overflow-y-auto max-h-[260px]">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-zinc-500 text-left">
                  <th className="pb-2 font-medium">Token</th>
                  <th className="pb-2 font-medium text-right">On-Chain Vol (USD)</th>
                  <th className="pb-2 font-medium text-right">Transfers</th>
                  <th className="pb-2 font-medium text-right">Minted</th>
                  <th className="pb-2 font-medium text-right">Burned</th>
                  <th className="pb-2 font-medium text-right">Net Issued (USD)</th>
                </tr>
              </thead>
              <tbody className="font-mono">
                {(data.major_tokens ?? []).map(m => (
                  <tr key={m.token_address} className="border-t border-zinc-800/40 text-zinc-300">
                    <td className="py-1.5">
                      <button
                        type="button"
                        onClick={() => onInspect?.(m.token_address, 'eth')}
                        title={`${m.token_address} — inspect in Address Risk`}
                        className="text-white font-semibold hover:underline decoration-zinc-600"
                      >
                        {m.symbol}
                      </button>
                      <span className="text-zinc-600 ml-1.5 font-sans text-[10px] uppercase">{m.category}</span>
                    </td>
                    <td className="py-1.5 text-right text-white font-semibold">${fmtNum(m.volume_usd)}</td>
                    <td className="py-1.5 text-right text-zinc-400">{fmtNum(m.transfers)}</td>
                    <td className="py-1.5 text-right text-zinc-400">
                      {m.mint_burn_supported ? fmtNum(m.minted_token) : <span className="text-zinc-600">n/a</span>}
                    </td>
                    <td className="py-1.5 text-right text-zinc-400">
                      {m.mint_burn_supported ? fmtNum(m.burned_token) : <span className="text-zinc-600">n/a</span>}
                    </td>
                    <td className="py-1.5 text-right">
                      {!m.mint_burn_supported ? (
                        <span
                          className="text-zinc-600 font-sans text-[11px]"
                          title={
                            m.symbol === 'USDT'
                              ? 'Tether emits Issue/Redeem events instead of 0x0 Transfer events'
                              : 'WETH emits Deposit/Withdrawal events instead of 0x0 Transfer events'
                          }
                        >
                          n/a ({m.symbol === 'USDT' ? 'Issue/Redeem' : 'Deposit/Withdraw'})
                        </span>
                      ) : (
                        <span className={m.net_issued_usd >= 0 ? 'text-emerald-400' : 'text-rose-400'}>
                          {m.net_issued_usd >= 0 ? '+' : '-'}${fmtNum(Math.abs(m.net_issued_usd))}
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Panel>
      </div>

      {/* Token vs Native Activity (with Zero-Value Split) & New Contracts Deployed */}
      <div className="grid lg:grid-cols-2 gap-4">
        <Panel
          title="Token vs Native Activity (Value vs Zero-Value Split)"
          note="non-zero vs zero-value Transfer events (incl. ERC-721 #0 &amp; poisoning) vs ETH transactions"
        >
          <ReactECharts
            style={{ height: 260 }}
            option={{
              tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
              legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
              grid: { left: 60, right: 20, top: 32, bottom: 24 },
              xAxis: { type: 'category', data: data.daily.map(r => r.date), axisLabel: AXIS_LABEL },
              yAxis: {
                type: 'value',
                axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtNum(v) },
                splitLine: SPLIT_LINE,
              },
              series: [
                {
                  name: 'Non-zero transfers',
                  type: 'bar',
                  stack: 'transfers',
                  data: data.daily.map(r => Math.max(0, r.transfers - (r.zero_value_transfers ?? 0))),
                  itemStyle: { color: '#a855f7' },
                },
                {
                  name: 'Zero-value transfers (incl. ERC-721 #0 & poisoning)',
                  type: 'bar',
                  stack: 'transfers',
                  data: data.daily.map(r => r.zero_value_transfers ?? 0),
                  itemStyle: { color: '#f59e0b' },
                },
                {
                  name: 'ETH transactions (all)',
                  type: 'line',
                  showSymbol: false,
                  data: data.daily.map(r => r.native_txs),
                  lineStyle: { color: ETH_COLOR, width: 2 },
                  itemStyle: { color: ETH_COLOR },
                },
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
              yAxis: {
                type: 'value',
                axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtNum(v) },
                splitLine: SPLIT_LINE,
              },
              series: [
                { name: 'All contracts', type: 'bar', data: data.contracts.map(r => r.contracts), itemStyle: { color: '#3f3f46' } },
                { name: 'ERC-20', type: 'bar', data: data.contracts.map(r => r.erc20), itemStyle: { color: '#a855f7' } },
                { name: 'ERC-721', type: 'bar', data: data.contracts.map(r => r.erc721), itemStyle: { color: '#22c55e' } },
              ],
            }}
          />
        </Panel>
      </div>

      {/* Token Movement Treemap (with metric toggle) & Enriched Top 25 Tokens Table */}
      <div className="grid lg:grid-cols-2 gap-4">
        <Panel
          title={
            <span className="inline-flex items-center gap-2">
              <span>Token Movement</span>
              <span className="inline-flex rounded-lg bg-zinc-900 p-0.5 border border-zinc-800">
                {(['transfers', 'senders'] as const).map(m => (
                  <button
                    key={m}
                    type="button"
                    onClick={() => setTreemapMetric(m)}
                    className={`px-2 py-0.5 rounded text-[11px] font-medium transition-colors ${
                      treemapMetric === m ? 'bg-zinc-700 text-white' : 'text-zinc-500 hover:text-zinc-300'
                    }`}
                  >
                    {m === 'transfers' ? 'By Transfers' : 'By Senders'}
                  </button>
                ))}
              </span>
            </span>
          }
          note="top 25 token contracts; amber border indicates local risk or counterfeit clue"
        >
          <ReactECharts
            style={{ height: 360 }}
            option={{
              tooltip: {
                ...CHART_TOOLTIP,
                formatter: (p: { name: string; value: number; data?: { addr?: string; zeroPct?: string } }) =>
                  `${p.name}${p.data?.addr ? ` (${shortHash(p.data.addr)})` : ''}<br/>${fmtNum(p.value)} ${treemapMetric}${
                    p.data?.zeroPct ? `<br/>Zero-value: ${p.data.zeroPct}%` : ''
                  }`,
              },
              series: [{
                type: 'treemap',
                roam: false,
                nodeClick: false,
                breadcrumb: { show: false },
                label: { color: '#e4e4e7', fontSize: 11, fontFamily: 'JetBrains Mono, monospace' },
                itemStyle: { borderColor: '#111114', borderWidth: 2, gapWidth: 2 },
                data: data.top_tokens.map((t, i) => {
                  const hasRisk = (t.risk && t.risk.length > 0) || (t.creator_risk && t.creator_risk.length > 0);
                  const zeroPct = t.transfers > 0 ? ((t.zero_value_transfers / t.transfers) * 100).toFixed(1) : '0.0';
                  return {
                    name: tokenLabel(t),
                    value: treemapMetric === 'senders' ? t.senders : t.transfers,
                    addr: t.token_address,
                    zeroPct,
                    itemStyle: {
                      color: hasRisk
                        ? '#ef4444'
                        : ['#a855f7', '#627eea', '#22c55e', '#fbbf24', '#38bdf8'][i % 5],
                      opacity: 0.55 + 0.45 * (1 - i / 25),
                    },
                  };
                }),
              }],
            }}
          />
        </Panel>

        <Panel
          title="Top Tokens by Activity, Metadata &amp; Risk"
          note="tokens_latest metadata (cached 7d) + local Scam Radar / OFAC clues · click address to inspect"
        >
          {data.top_tokens.length === 0 ? <EmptyState text="No token transfers in range." /> : (
            <div className="overflow-y-auto max-h-[360px]">
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-zinc-500 text-left">
                    <th className="pb-2 font-medium">#</th>
                    <th className="pb-2 font-medium">Token / Standard</th>
                    <th className="pb-2 font-medium">Creator</th>
                    <th className="pb-2 font-medium text-right">Transfers (0-val%)</th>
                    <th className="pb-2 font-medium text-right">≈ Senders</th>
                    <th className="pb-2 font-medium text-right">≈ Receivers</th>
                  </tr>
                </thead>
                <tbody className="font-mono">
                  {data.top_tokens.map((t, i) => {
                    const zeroPct = t.transfers > 0 ? (t.zero_value_transfers / t.transfers) * 100 : 0;
                    return (
                      <tr key={t.token_address} className="border-t border-zinc-800/40 text-zinc-300">
                        <td className="py-1.5 text-zinc-600">{i + 1}</td>
                        <td className="py-1.5">
                          <div className="flex items-center flex-wrap gap-1">
                            <span className="text-white font-semibold">{tokenLabel(t)}</span>
                            <button
                              type="button"
                              onClick={() => onInspect?.(t.token_address, 'eth')}
                              title={`${t.token_address} — check in Address Risk`}
                              className="text-zinc-500 hover:text-white underline decoration-zinc-700"
                            >
                              {shortHash(t.token_address)}
                            </button>
                            {t.token_standard && (
                              <span className="rounded bg-zinc-800 px-1 text-[10px] text-zinc-400 font-sans">
                                {t.token_standard}
                              </span>
                            )}
                            {t.proxy_standard && (
                              <span className="rounded bg-indigo-950/60 border border-indigo-800/50 px-1 text-[10px] text-indigo-300 font-sans">
                                {t.proxy_standard}
                              </span>
                            )}
                            <RiskBadges codes={t.risk} />
                          </div>
                        </td>
                        <td className="py-1.5">
                          {t.creator_address ? (
                            <span className="inline-flex items-center flex-wrap">
                              <button
                                type="button"
                                onClick={() => onInspect?.(t.creator_address!, 'eth')}
                                title={`Creator ${t.creator_address}${t.created_at ? ` (deployed ${t.created_at})` : ''} — check in Address Risk`}
                                className="text-zinc-500 hover:text-white underline decoration-zinc-700"
                              >
                                {shortHash(t.creator_address)}
                              </button>
                              <RiskBadges codes={t.creator_risk} />
                            </span>
                          ) : (
                            <span className="text-zinc-600">—</span>
                          )}
                        </td>
                        <td className="py-1.5 text-right">
                          <span className="font-semibold text-white">{fmtNum(t.transfers)}</span>
                          {zeroPct > 0 && (
                            <span
                              className={`ml-1.5 text-[10px] ${
                                zeroPct >= 25 ? 'text-amber-400' : 'text-zinc-500'
                              }`}
                              title={`${fmtNum(t.zero_value_transfers)} zero-value transfers`}
                            >
                              ({zeroPct.toFixed(0)}%)
                            </span>
                          )}
                        </td>
                        <td className="py-1.5 text-right text-zinc-500">{fmtNum(t.senders)}</td>
                        <td className="py-1.5 text-right text-zinc-500">{fmtNum(t.receivers)}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
      </div>
    </div>
  );
}

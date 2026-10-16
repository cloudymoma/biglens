import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import type { AddressRiskChain, CryptoTokensLiveData } from '../../types';
import { fetchCryptoTokensLive } from '../../api';
import { ago } from './addressRiskTools';
import {
  CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE,
  fmtNum, shortHash, Panel,
} from './shared';

const NETWORK_ORDER = ['tron', 'eth', 'sol', 'arb', 'op', 'base'] as const;

export default function TokensLivePanel({
  onInspect,
}: {
  onInspect?: (address: string, chain?: AddressRiskChain) => void;
}) {
  const [live, setLive] = useState<CryptoTokensLiveData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    let stale = false;
    fetchCryptoTokensLive()
      .then(d => { if (!stale) setLive(d); })
      .catch(e => {
        if (!stale) {
          setError(typeof e.response?.data === 'string' && e.response.data.trim() ? e.response.data.trim() : (e.message || 'request failed'));
        }
      })
      .finally(() => { if (!stale) setLoading(false); });
    return () => { stale = true; };
  }, []);

  if (loading && !live) {
    return (
      <div className="rounded-2xl border border-zinc-800/50 p-4 text-xs text-zinc-500" style={{ background: '#111114' }}>
        Loading live cross-chain stablecoin supply &amp; market pulse…
      </div>
    );
  }
  if (error || !live) {
    return (
      <div className="rounded-2xl border border-amber-500/30 bg-amber-500/5 px-4 py-3 text-xs text-amber-300">
        Live supply unavailable: {error || 'empty response'}
      </div>
    );
  }

  const supplyMatrix = live.supply_matrix ?? [];
  const marketPulse = live.market_pulse ?? [];

  const netLabels = NETWORK_ORDER.map(net => {
    const found = supplyMatrix.find(r => r.network === net);
    return found?.network_label ?? net.toUpperCase();
  });

  const sumSupply = (asset: string, net: string, tier: string) =>
    supplyMatrix
      .filter(r => r.asset === asset && r.network === net && r.tier === tier)
      .reduce((acc, r) => acc + r.supply, 0);

  const totalUSDT = supplyMatrix.filter(r => r.asset === 'USDT').reduce((a, r) => a + r.supply, 0);
  const totalUSDC = supplyMatrix.filter(r => r.asset === 'USDC').reduce((a, r) => a + r.supply, 0);

  return (
    <div className="grid lg:grid-cols-2 gap-4">
      <Panel
        title="Live Cross-Chain Stablecoin Supply (USDT vs USDC)"
        note={`totalSupply() via keyless RPCs · USDT $${fmtNum(totalUSDT)} · USDC $${fmtNum(totalUSDC)} · ${live.as_of ? ago(live.as_of) : 'live'}`}
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
            grid: { left: 64, right: 20, top: 32, bottom: 28 },
            xAxis: { type: 'category', data: netLabels, axisLabel: AXIS_LABEL },
            yAxis: {
              type: 'value',
              axisLabel: { ...AXIS_LABEL, formatter: (v: number) => `$${fmtNum(v)}` },
              splitLine: SPLIT_LINE,
            },
            series: [
              {
                name: 'USDT (native)',
                type: 'bar',
                stack: 'USDT',
                data: NETWORK_ORDER.map(n => sumSupply('USDT', n, 'native')),
                itemStyle: { color: '#22c55e' },
              },
              {
                name: 'USDT (bridged)',
                type: 'bar',
                stack: 'USDT',
                data: NETWORK_ORDER.map(n => sumSupply('USDT', n, 'bridged')),
                itemStyle: { color: '#86efac' },
              },
              {
                name: 'USDC (native)',
                type: 'bar',
                stack: 'USDC',
                data: NETWORK_ORDER.map(n => sumSupply('USDC', n, 'native')),
                itemStyle: { color: '#3b82f6' },
              },
              {
                name: 'USDC (bridged)',
                type: 'bar',
                stack: 'USDC',
                data: NETWORK_ORDER.map(n => sumSupply('USDC', n, 'bridged')),
                itemStyle: { color: '#93c5fd' },
              },
            ],
          }}
        />
      </Panel>

      <Panel
        title="Live Token Market Pulse (Ethereum)"
        note="DefiLlama spot USD price + Blockscout v2 holders, 24h volume &amp; circulating market cap"
      >
        <div className="overflow-y-auto max-h-[260px]">
          <table className="w-full text-xs">
            <thead>
              <tr className="text-zinc-500 text-left">
                <th className="pb-2 font-medium">Token</th>
                <th className="pb-2 font-medium text-right">Spot Price</th>
                <th className="pb-2 font-medium text-right">Market Cap</th>
                <th className="pb-2 font-medium text-right">24h Vol (API)</th>
                <th className="pb-2 font-medium text-right">Holders</th>
              </tr>
            </thead>
            <tbody className="font-mono">
              {marketPulse.map(m => (
                <tr key={m.address} className="border-t border-zinc-800/40 text-zinc-300">
                  <td className="py-1.5">
                    <span className="text-white font-semibold">{m.symbol}</span>
                    <button
                      type="button"
                      onClick={() => onInspect?.(m.address, 'eth')}
                      title={`${m.address} — inspect in Address Risk`}
                      className="text-zinc-500 hover:text-white underline decoration-zinc-700 ml-2"
                    >
                      {shortHash(m.address)}
                    </button>
                    {m.stale && (
                      <span className="ml-1.5 text-[10px] text-amber-400/80 font-sans">stale</span>
                    )}
                  </td>
                  <td className="py-1.5 text-right text-white">
                    {m.price_usd > 0
                      ? `$${m.price_usd >= 100 ? m.price_usd.toLocaleString(undefined, { maximumFractionDigits: 2 }) : m.price_usd.toFixed(4)}`
                      : '—'}
                  </td>
                  <td className="py-1.5 text-right text-zinc-300">
                    {m.market_cap_usd > 0 ? `$${fmtNum(m.market_cap_usd)}` : '—'}
                  </td>
                  <td className="py-1.5 text-right text-zinc-400">
                    {m.volume_24h_usd > 0 ? `$${fmtNum(m.volume_24h_usd)}` : '—'}
                  </td>
                  <td className="py-1.5 text-right text-zinc-400">
                    {m.holders_count > 0 ? fmtNum(m.holders_count) : '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Panel>
    </div>
  );
}

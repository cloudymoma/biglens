import { useCallback, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { CheckCircle2, Clock, Download } from 'lucide-react';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import { AXIS_LABEL, CHART_TOOLTIP, Panel, SPLIT_LINE } from '../crypto/shared';
import GasLiveBar from '../crypto/GasLiveBar';
import HelpTip from '../crypto/HelpTip';
import { fetchBitcoinFees, fetchBitcoinOnchain } from './api';
import {
  downloadCsv,
  fmtBTCFromSats,
  fmtPct,
  fmtSats,
  fmtSatVB,
  fmtUSD,
  satsToBTC,
} from './format';
import { usePolling } from './usePolling';
import type { BitcoinBigQueryControl } from './useBitcoinBigQuery';
import type { BtcMetaBigQuery, BtcOnchainDay } from './types';
import BigQueryLocked from './BigQueryLocked';
import SourceBadge from './SourceBadge';

const MEMPOOL_PERIODS: { id: '24h' | '1w' | '1m'; label: string }[] = [
  { id: '24h', label: '24H' },
  { id: '1w', label: '1W' },
  { id: '1m', label: '1M' },
];

export default function FeesTab({
  days,
  bqControl,
  bigquery,
  refreshTick,
}: {
  days: number;
  bqControl: BitcoinBigQueryControl;
  bigquery: BtcMetaBigQuery | null;
  refreshTick: number;
}) {
  const [period, setPeriod] = useState<'24h' | '1w' | '1m'>('24h');

  const fetchFees = useCallback(() => fetchBitcoinFees(period), [period]);
  const feesPoll = usePolling(fetchFees, 60_000, `btc-fees-${period}-${refreshTick}`);

  const fetchOnchain = useCallback(() => fetchBitcoinOnchain(days), [days]);
  const onchainPoll = usePolling(
    fetchOnchain,
    60_000,
    `btc-onchain-${days}-${refreshTick}`,
    bqControl.show,
  );

  const feesData = feesPoll.data;
  const onchainRows: BtcOnchainDay[] = onchainPoll.data?.rows ?? [];
  const latestDay = onchainRows.length > 0 ? onchainRows[onchainRows.length - 1] : null;

  const mempoolDates = (feesData?.mempool_history ?? []).map(p => p.ts.slice(5, 16).replace('T', ' '));
  const mempoolBandsOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 11 },
      data: ['0–1 sat/vB', '1–2 sat/vB', '2–5 sat/vB', '5–10 sat/vB', '10–50 sat/vB', '50+ sat/vB'],
    },
    grid: { left: 48, right: 16, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: mempoolDates, axisLabel: AXIS_LABEL },
    yAxis: {
      type: 'value',
      name: 'vMB',
      nameTextStyle: AXIS_LABEL,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    series: [
      {
        name: '0–1 sat/vB',
        type: 'line',
        stack: 'vmb',
        areaStyle: { opacity: 0.35 },
        showSymbol: false,
        itemStyle: { color: '#34d399' },
        data: (feesData?.mempool_history ?? []).map(p => Number(p.band_0_1_vmb.toFixed(2))),
      },
      {
        name: '1–2 sat/vB',
        type: 'line',
        stack: 'vmb',
        areaStyle: { opacity: 0.35 },
        showSymbol: false,
        itemStyle: { color: '#a3e635' },
        data: (feesData?.mempool_history ?? []).map(p => Number(p.band_1_2_vmb.toFixed(2))),
      },
      {
        name: '2–5 sat/vB',
        type: 'line',
        stack: 'vmb',
        areaStyle: { opacity: 0.35 },
        showSymbol: false,
        itemStyle: { color: '#facc15' },
        data: (feesData?.mempool_history ?? []).map(p => Number(p.band_2_5_vmb.toFixed(2))),
      },
      {
        name: '5–10 sat/vB',
        type: 'line',
        stack: 'vmb',
        areaStyle: { opacity: 0.35 },
        showSymbol: false,
        itemStyle: { color: '#fb923c' },
        data: (feesData?.mempool_history ?? []).map(p => Number(p.band_5_10_vmb.toFixed(2))),
      },
      {
        name: '10–50 sat/vB',
        type: 'line',
        stack: 'vmb',
        areaStyle: { opacity: 0.35 },
        showSymbol: false,
        itemStyle: { color: '#f87171' },
        data: (feesData?.mempool_history ?? []).map(p => Number(p.band_10_50_vmb.toFixed(2))),
      },
      {
        name: '50+ sat/vB',
        type: 'line',
        stack: 'vmb',
        areaStyle: { opacity: 0.35 },
        showSymbol: false,
        itemStyle: { color: '#e11d48' },
        data: (feesData?.mempool_history ?? []).map(p => Number(p.band_50_plus_vmb.toFixed(2))),
      },
    ],
  };

  const blockRateLabels = (feesData?.block_fee_rates ?? []).map(p => `#${p.avg_height}`);
  const blockFeeRatesOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 11 },
      data: ['p10', 'p25', 'p50 (median)', 'p75', 'p90'],
    },
    grid: { left: 48, right: 16, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: blockRateLabels, axisLabel: AXIS_LABEL },
    yAxis: {
      type: 'value',
      name: 'sat/vB',
      nameTextStyle: AXIS_LABEL,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    series: [
      {
        name: 'p10',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#34d399' },
        data: (feesData?.block_fee_rates ?? []).map(p => p.p10_sat_vb),
      },
      {
        name: 'p25',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#38bdf8' },
        data: (feesData?.block_fee_rates ?? []).map(p => p.p25_sat_vb),
      },
      {
        name: 'p50 (median)',
        type: 'line',
        showSymbol: false,
        lineStyle: { width: 2 },
        itemStyle: { color: '#f7931a' },
        data: (feesData?.block_fee_rates ?? []).map(p => p.p50_sat_vb),
      },
      {
        name: 'p75',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#fb923c' },
        data: (feesData?.block_fee_rates ?? []).map(p => p.p75_sat_vb),
      },
      {
        name: 'p90',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#f87171' },
        data: (feesData?.block_fee_rates ?? []).map(p => p.p90_sat_vb),
      },
    ],
  };

  const onchainDates = onchainRows.map(r => r.date);
  const blockSpaceOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 11 },
      data: ['Payment', 'Batch', 'Consolidation', 'Inscription-like', 'Runes', 'OP_RETURN', 'Coinbase'],
    },
    grid: { left: 48, right: 16, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: onchainDates, axisLabel: AXIS_LABEL },
    yAxis: {
      type: 'value',
      max: 100,
      name: '% of vbytes',
      nameTextStyle: AXIS_LABEL,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    series: [
      {
        name: 'Payment',
        type: 'bar',
        stack: 'vb',
        itemStyle: { color: '#38bdf8' },
        data: onchainRows.map(r => Number(((r.vb_payment / Math.max(1, r.vbytes)) * 100).toFixed(2))),
      },
      {
        name: 'Batch',
        type: 'bar',
        stack: 'vb',
        itemStyle: { color: '#34d399' },
        data: onchainRows.map(r => Number(((r.vb_batch / Math.max(1, r.vbytes)) * 100).toFixed(2))),
      },
      {
        name: 'Consolidation',
        type: 'bar',
        stack: 'vb',
        itemStyle: { color: '#a3e635' },
        data: onchainRows.map(r => Number(((r.vb_consolidation / Math.max(1, r.vbytes)) * 100).toFixed(2))),
      },
      {
        name: 'Inscription-like',
        type: 'bar',
        stack: 'vb',
        itemStyle: { color: '#f59e0b' },
        data: onchainRows.map(r => Number(((r.vb_inscription_like / Math.max(1, r.vbytes)) * 100).toFixed(2))),
      },
      {
        name: 'Runes',
        type: 'bar',
        stack: 'vb',
        itemStyle: { color: '#ec4899' },
        data: onchainRows.map(r => Number(((r.vb_runes / Math.max(1, r.vbytes)) * 100).toFixed(2))),
      },
      {
        name: 'OP_RETURN',
        type: 'bar',
        stack: 'vb',
        itemStyle: { color: '#a855f7' },
        data: onchainRows.map(r => Number(((r.vb_op_return / Math.max(1, r.vbytes)) * 100).toFixed(2))),
      },
      {
        name: 'Coinbase',
        type: 'bar',
        stack: 'vb',
        itemStyle: { color: '#71717a' },
        data: onchainRows.map(r => Number(((r.vb_coinbase / Math.max(1, r.vbytes)) * 100).toFixed(2))),
      },
    ],
  };

  const scriptAdoptionOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 11 },
      data: ['P2TR (Taproot)', 'P2WPKH', 'P2WSH', 'P2SH', 'P2PKH', 'Pay-to-Anchor'],
    },
    grid: { left: 48, right: 16, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: onchainDates, axisLabel: AXIS_LABEL },
    yAxis: {
      type: 'value',
      name: 'Outputs',
      nameTextStyle: AXIS_LABEL,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    series: [
      {
        name: 'P2TR (Taproot)',
        type: 'line',
        stack: 'out',
        areaStyle: { opacity: 0.35 },
        showSymbol: false,
        itemStyle: { color: '#f7931a' },
        data: onchainRows.map(r => r.out_p2tr),
      },
      {
        name: 'P2WPKH',
        type: 'line',
        stack: 'out',
        areaStyle: { opacity: 0.35 },
        showSymbol: false,
        itemStyle: { color: '#38bdf8' },
        data: onchainRows.map(r => r.out_p2wpkh),
      },
      {
        name: 'P2WSH',
        type: 'line',
        stack: 'out',
        areaStyle: { opacity: 0.35 },
        showSymbol: false,
        itemStyle: { color: '#34d399' },
        data: onchainRows.map(r => r.out_p2wsh),
      },
      {
        name: 'P2SH',
        type: 'line',
        stack: 'out',
        areaStyle: { opacity: 0.35 },
        showSymbol: false,
        itemStyle: { color: '#a855f7' },
        data: onchainRows.map(r => r.out_p2sh),
      },
      {
        name: 'P2PKH',
        type: 'line',
        stack: 'out',
        areaStyle: { opacity: 0.35 },
        showSymbol: false,
        itemStyle: { color: '#71717a' },
        data: onchainRows.map(r => r.out_p2pkh),
      },
      {
        name: 'Pay-to-Anchor',
        type: 'line',
        stack: 'out',
        areaStyle: { opacity: 0.35 },
        showSymbol: false,
        itemStyle: { color: '#f43f5e' },
        data: onchainRows.map(r => r.out_anchor),
      },
    ],
  };

  const handleExportOnchainCsv = () => {
    if (onchainRows.length === 0) return;
    downloadCsv(
      `bitcoin-onchain-fees-${days}d.csv`,
      [
        'date',
        'blocks',
        'txs',
        'fees_btc',
        'fee_share_pct',
        'fee_p50_sat_vb',
        'witness_share_pct',
        'vb_payment',
        'vb_inscription_like',
        'vb_runes',
        'vb_op_return',
        'runestones',
        'out_p2tr',
        'out_anchor',
      ],
      onchainRows.map(r => [
        r.date,
        r.blocks,
        r.txs,
        satsToBTC(r.fees_sats).toFixed(8),
        r.fee_share_pct.toFixed(2),
        r.fee_p50_sat_vb.toFixed(2),
        r.witness_share_pct.toFixed(2),
        r.vb_payment,
        r.vb_inscription_like,
        r.vb_runes,
        r.vb_op_return,
        r.runestones,
        r.out_p2tr,
        r.out_anchor,
      ]),
    );
  };

  return (
    <div className="space-y-6">
      {/* 1. Embedded Live Fee Market */}
      <GasLiveBar chain="btc" />

      {feesPoll.error && <ErrorBanner message={feesPoll.error} />}

      {/* 2. Consolidation Window & Transaction Cost Calculator */}
      {feesData && (
        <div className="grid gap-4 lg:grid-cols-3">
          <Panel
            title={
              <span className="inline-flex items-center gap-1.5">
                <span>UTXO Consolidation Window</span>
                <HelpTip label="Consolidation signal rule">
                  Window is open when economy fee ≤ {feesData.consolidation.threshold_sat_vb} sat/vB and
                  mempool backlog ≤ {feesData.consolidation.max_blocks_to_clear} blocks (~3 vMB).
                </HelpTip>
              </span>
            }
            note="Descriptive rule · mempool.space"
          >
            <div className="space-y-3">
              <div className="flex items-center gap-2.5">
                {feesData.consolidation.economy_sat_vb <= 0 ? (
                  <span className="inline-flex items-center gap-1.5 rounded-full border border-zinc-700/60 bg-zinc-800/60 px-3 py-1 text-xs font-semibold text-zinc-400">
                    <Clock className="h-4 w-4" />
                    <span>Unavailable</span>
                  </span>
                ) : feesData.consolidation.open ? (
                  <span className="inline-flex items-center gap-1.5 rounded-full border border-emerald-500/30 bg-emerald-500/15 px-3 py-1 text-xs font-semibold text-emerald-300">
                    <CheckCircle2 className="h-4 w-4" />
                    <span>Window Open</span>
                  </span>
                ) : (
                  <span className="inline-flex items-center gap-1.5 rounded-full border border-amber-500/30 bg-amber-500/15 px-3 py-1 text-xs font-semibold text-amber-300">
                    <Clock className="h-4 w-4" />
                    <span>Window Closed</span>
                  </span>
                )}
              </div>
              <p className="text-xs leading-relaxed text-zinc-300">
                {feesData.consolidation.economy_sat_vb <= 0
                  ? 'Live fee market telemetry unavailable'
                  : feesData.consolidation.reason}
              </p>
              <div className="grid grid-cols-2 gap-2 pt-2 text-xs">
                <div className="rounded-lg border border-zinc-800/80 bg-zinc-950/60 p-2.5">
                  <div className="text-[11px] text-zinc-500">Economy Rate</div>
                  <div className="mt-0.5 font-mono font-semibold text-zinc-100">
                    {feesData.consolidation.economy_sat_vb <= 0
                      ? 'unavailable'
                      : fmtSatVB(feesData.consolidation.economy_sat_vb)}
                  </div>
                </div>
                <div className="rounded-lg border border-zinc-800/80 bg-zinc-950/60 p-2.5">
                  <div className="text-[11px] text-zinc-500">Backlog</div>
                  <div className="mt-0.5 font-mono font-semibold text-zinc-100">
                    {feesData.consolidation.economy_sat_vb <= 0
                      ? 'unavailable'
                      : `${feesData.consolidation.blocks_to_clear.toFixed(1)} blks`}
                  </div>
                </div>
              </div>
            </div>
          </Panel>

          <div className="lg:col-span-2">
            <Panel
              title={
                <span className="inline-flex items-center gap-1.5">
                  <span>Transaction Cost Calculator</span>
                  <HelpTip label="Transaction template vsizes">
                    Costs computed from standard virtual sizes (vbytes) multiplied by live mempool.space fee tiers.
                  </HelpTip>
                </span>
              }
              note={feesData.price_usd ? `BTC spot ${fmtUSD(feesData.price_usd, 0)}` : 'Native sats'}
            >
              <div className="overflow-x-auto">
                <table className="w-full text-left text-xs">
                  <thead>
                    <tr className="border-b border-zinc-800 text-[11px] text-zinc-400">
                      <th className="pb-2 font-medium">Template</th>
                      <th className="pb-2 text-right font-medium">vB</th>
                      <th className="pb-2 text-right font-medium">Next Block</th>
                      <th className="pb-2 text-right font-medium">~30 Min</th>
                      <th className="pb-2 text-right font-medium">Economy</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-zinc-800/60 font-mono">
                    {feesData.templates.map(tpl => {
                      const feeMarketUnavailable = feesData.consolidation.economy_sat_vb <= 0;
                      return (
                        <tr key={tpl.id} className="hover:bg-zinc-800/20">
                          <td className="py-2 font-sans text-zinc-200">{tpl.label}</td>
                          <td className="py-2 text-right text-zinc-400">{tpl.vbytes.toLocaleString()}</td>
                          <td className="py-2 text-right text-zinc-100">
                            {feeMarketUnavailable || tpl.next_block_sats <= 0 ? (
                              <span className="text-zinc-500">unavailable</span>
                            ) : (
                              <>
                                {fmtSats(tpl.next_block_sats)}
                                {tpl.next_block_usd !== null && (
                                  <span className="ml-1.5 text-[11px] text-zinc-400">
                                    ({fmtUSD(tpl.next_block_usd, 2)})
                                  </span>
                                )}
                              </>
                            )}
                          </td>
                          <td className="py-2 text-right text-zinc-200">
                            {feeMarketUnavailable || tpl.half_hour_sats <= 0 ? (
                              <span className="text-zinc-500">unavailable</span>
                            ) : (
                              <>
                                {fmtSats(tpl.half_hour_sats)}
                                {tpl.half_hour_usd !== null && (
                                  <span className="ml-1.5 text-[11px] text-zinc-400">
                                    ({fmtUSD(tpl.half_hour_usd, 2)})
                                  </span>
                                )}
                              </>
                            )}
                          </td>
                          <td className="py-2 text-right text-emerald-300">
                            {feeMarketUnavailable || tpl.economy_sats <= 0 ? (
                              <span className="text-zinc-500">unavailable</span>
                            ) : (
                              <>
                                {fmtSats(tpl.economy_sats)}
                                {tpl.economy_usd !== null && (
                                  <span className="ml-1.5 text-[11px] text-zinc-400">
                                    ({fmtUSD(tpl.economy_usd, 2)})
                                  </span>
                                )}
                              </>
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </Panel>
          </div>
        </div>
      )}

      {/* 3. Mempool History & Block Fee-Rate Percentiles */}
      <div className="flex items-center justify-between">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs font-medium text-zinc-400">Mempool &amp; Mined Blocks Window:</span>
          <div className="inline-flex rounded-lg border border-zinc-800 bg-zinc-900/80 p-0.5 text-xs">
            {MEMPOOL_PERIODS.map(p => (
              <button
                key={p.id}
                type="button"
                onClick={() => setPeriod(p.id)}
                className={`rounded-md px-2.5 py-1 font-medium transition ${
                  period === p.id
                    ? 'bg-amber-500 text-zinc-950'
                    : 'text-zinc-400 hover:text-zinc-200'
                }`}
              >
                {p.label}
              </button>
            ))}
          </div>
        </div>
        {feesData?.sources && (
          <div className="flex flex-wrap items-center gap-1.5">
            {feesData.sources.map(s => (
              <SourceBadge key={s.id} source={s} />
            ))}
          </div>
        )}
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              <span>Mempool Backlog by Fee Band (vMB)</span>
              <HelpTip label="Mempool fee bands">
                39 mempool.space fee-rate buckets folded into 6 display bands in virtual megabytes (1 vMB = 10⁶ vB).
              </HelpTip>
            </span>
          }
          note={`Period: ${period}`}
        >
          {feesData && feesData.mempool_history.length > 0 ? (
            <ReactECharts option={mempoolBandsOption} style={{ height: 280 }} notMerge />
          ) : (
            <EmptyState text="Loading mempool history…" />
          )}
        </Panel>

        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              <span>Mined-Block Fee Percentiles (sat/vB)</span>
              <HelpTip label="Block fee percentiles">
                Per-block fee-rate percentiles (p10, p25, p50, p75, p90) reported by mempool.space.
              </HelpTip>
            </span>
          }
          note="mempool.space"
        >
          {feesData && feesData.block_fee_rates.length > 0 ? (
            <ReactECharts option={blockFeeRatesOption} style={{ height: 280 }} notMerge />
          ) : (
            <EmptyState text="Loading block fee rates…" />
          )}
        </Panel>
      </div>

      {/* 4. BigQuery On-Chain Block-Space & Script Adoption */}
      {!bqControl.show ? (
        <BigQueryLocked
          title="On-Chain Block Space, Runes/Inscriptions & Script Adoption"
          description="Daily transaction-weighted fee percentiles, block-space vbytes breakdown (payments, batches, consolidations, Runes, OP_RETURN, inscription-like), witness share, script-type adoption (including Pay-to-Anchor), and output value bands."
          bigquery={bigquery}
          onEnableDisplay={bqControl.available ? bqControl.toggle : undefined}
        />
      ) : onchainRows.length === 0 ? (
        <Panel title="BigQuery On-Chain Block Space" note="SQLite read-only">
          <EmptyState text={onchainPoll.loading ? 'Loading on-chain rows…' : 'No on-chain rollup rows stored yet.'} />
        </Panel>
      ) : (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs font-semibold text-amber-300">
                BigQuery On-Chain Accounting ({onchainRows.length} days · latest {latestDay?.date})
              </span>
              {onchainPoll.data?.sources?.map(s => (
                <SourceBadge key={s.id} source={s} />
              ))}
            </div>
            <button
              type="button"
              onClick={handleExportOnchainCsv}
              className="inline-flex items-center gap-1.5 rounded-lg border border-zinc-800 bg-zinc-900/80 px-2.5 py-1 text-xs text-zinc-300 transition hover:border-zinc-700 hover:text-zinc-100"
            >
              <Download className="h-3.5 w-3.5" />
              <span>Export CSV</span>
            </button>
          </div>

          {latestDay && (
            <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
              <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3">
                <div className="text-[11px] text-zinc-400">Daily Fees ({latestDay.date})</div>
                <div className="mt-1 font-mono text-base font-bold text-zinc-100">
                  {fmtBTCFromSats(latestDay.fees_sats, 2)}
                </div>
                <div className="mt-0.5 text-[11px] text-zinc-400">
                  {fmtPct(latestDay.fee_share_pct, 2)} of miner revenue
                </div>
              </div>
              <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3">
                <div className="text-[11px] text-zinc-400">Witness Share</div>
                <div className="mt-1 font-mono text-base font-bold text-zinc-100">
                  {fmtPct(latestDay.witness_share_pct, 1)}
                </div>
                <div className="mt-0.5 text-[11px] text-zinc-400">
                  {(latestDay.witness_bytes / 1e6).toFixed(1)} MB witness / {(latestDay.bytes / 1e6).toFixed(1)} MB total
                </div>
              </div>
              <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3">
                <div className="text-[11px] text-zinc-400">Runestones / OP_RETURN</div>
                <div className="mt-1 font-mono text-base font-bold text-zinc-100">
                  {latestDay.runestones.toLocaleString()} / {latestDay.opret_outputs.toLocaleString()}
                </div>
                <div className="mt-0.5 text-[11px] text-zinc-400">
                  {latestDay.opret_large.toLocaleString()} outputs &gt; 83 B · burned {fmtSats(latestDay.opret_value_sats)}
                </div>
              </div>
              <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3">
                <div className="text-[11px] text-zinc-400">Pay-to-Anchor (P2A)</div>
                <div className="mt-1 font-mono text-base font-bold text-zinc-100">
                  {latestDay.out_anchor.toLocaleString()} outs · {latestDay.in_anchor.toLocaleString()} ins
                </div>
                <div className="mt-0.5 text-[11px] text-zinc-400">
                  Active addresses: {latestDay.active_addresses.toLocaleString()}
                </div>
              </div>
            </div>
          )}

          <div className="grid gap-4 lg:grid-cols-2">
            <Panel
              title={
                <span className="inline-flex items-center gap-1.5">
                  <span>Block-Space Composition (% of vbytes)</span>
                  <HelpTip label="Block-space classification heuristics">
                    Precedence: coinbase → Runes (OP_RETURN 0x6a5d) → OP_RETURN → inscription-like (heuristic: all
                    inputs taproot and ≥ 150 witness bytes/input; M0 verified 93.9% precision and 99.95% recall by
                    vbytes against ord-envelope ground truth) → consolidation (≥ 5-in / 1-out) → batch (≥ 10-out) →
                    payment.
                  </HelpTip>
                </span>
              }
              note="Heuristic classes labeled"
            >
              <ReactECharts option={blockSpaceOption} style={{ height: 280 }} notMerge />
            </Panel>

            <Panel
              title={
                <span className="inline-flex items-center gap-1.5">
                  <span>Script-Type Output Adoption</span>
                  <HelpTip label="Script-type outputs">
                    Daily outputs created by script type, including Taproot (P2TR) and ephemeral Pay-to-Anchor (P2A)
                    outputs.
                  </HelpTip>
                </span>
              }
              note="Outputs per UTC day"
            >
              <ReactECharts option={scriptAdoptionOption} style={{ height: 280 }} notMerge />
            </Panel>
          </div>
        </div>
      )}
    </div>
  );
}

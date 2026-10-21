import { useCallback, useMemo, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { Cpu, Download, Globe2, Pickaxe, Shield, Zap } from 'lucide-react';
import type {
  BtcConcentrationMetric,
  BtcMetaBigQuery,
  BtcMiningData,
  BtcMiningPeriod,
} from './types';
import type { BitcoinBigQueryControl } from './useBitcoinBigQuery';
import { fetchBitcoinMining } from './api';
import { usePolling } from '../common/usePolling';
import SourceBadge from '../common/SourceBadge';
import BigQueryLocked from './BigQueryLocked';
import NodeMap from './NodeMap';
import {
  RIGS,
  breakEvenKwh,
  btcPerThDay,
  costPerBtc,
  type Assumptions,
} from './miningMath';
import { EmptyState, ErrorBanner, MetricCard } from '../../dashboards/shared';
import {
  AXIS_LABEL,
  BTC_COLOR,
  CHART_TOOLTIP,
  Panel,
  SPLIT_LINE,
  fmtNum,
} from '../crypto/shared';
import HelpTip from '../crypto/HelpTip';
import { downloadCsv, fmtPct, fmtUSD } from '../common/format';
import { fmtBTCFromSats } from './format';

interface MiningTabProps {
  bqControl: BitcoinBigQueryControl;
  bigquery: BtcMetaBigQuery | null;
  refreshTick: number;
}

const PERIODS: { id: BtcMiningPeriod; label: string }[] = [
  { id: '3m', label: '3M' },
  { id: '1y', label: '1Y' },
  { id: '3y', label: '3Y' },
];

function ConcentrationChip({
  label,
  metric,
  formatValue,
}: {
  label: string;
  metric: BtcConcentrationMetric;
  formatValue: (v: number) => string;
}) {
  return (
    <div className="rounded-lg border border-zinc-800/80 bg-zinc-900/60 p-3">
      <div className="flex items-center justify-between gap-2">
        <span className="text-[11px] font-medium text-zinc-400">{label}</span>
        <span className="font-mono text-sm font-bold text-amber-300">
          {formatValue(metric.value)}
        </span>
      </div>
      <div className="mt-1 text-[11px] text-zinc-400">
        {metric.denominator.toLocaleString()} {metric.unit} ({metric.coverage_pct.toFixed(1)}% coverage)
      </div>
      <div className="mt-0.5 text-[10px] text-zinc-500">
        Method: {metric.method}
      </div>
    </div>
  );
}

function NumberInput({
  label,
  unit,
  value,
  onChange,
  width = 'w-24',
}: {
  label: string;
  unit: string;
  value: string;
  onChange: (v: string) => void;
  width?: string;
}) {
  return (
    <label className="flex items-center gap-2 text-xs text-zinc-400">
      {label}
      <input
        type="number"
        min="0"
        step="any"
        value={value}
        onChange={e => onChange(e.target.value)}
        className={`${width} rounded-lg border border-zinc-800 bg-zinc-900 px-2.5 py-1.5 text-xs text-white focus:border-zinc-600 focus:outline-none`}
      />
      <span className="text-zinc-500">{unit}</span>
    </label>
  );
}

export default function MiningTab({ bqControl, bigquery, refreshTick }: MiningTabProps) {
  const [period, setPeriod] = useState<BtcMiningPeriod>('1y');
  const [priceOverride, setPriceOverride] = useState<string>('');
  const [elecStr, setElecStr] = useState<string>('0.06');
  const [pueStr, setPueStr] = useState<string>('1.05');
  const [poolFeeStr, setPoolFeeStr] = useState<string>('1');
  const [customJthStr, setCustomJthStr] = useState<string>('18');

  const fetchMining = useCallback(() => fetchBitcoinMining(period), [period]);
  const miningPoll = usePolling<BtcMiningData>(
    fetchMining,
    600_000,
    `btc-mining-${period}-${refreshTick}`,
  );

  const data = miningPoll.data;

  const networkMixData = useMemo(() => {
    if (!data?.nodes) return [];
    const n = data.nodes.network;
    return [
      { name: 'Tor (.onion)', value: n.tor, itemStyle: { color: '#a855f7' } },
      { name: 'IPv4 Clearnet', value: n.ipv4, itemStyle: { color: '#f59e0b' } },
      { name: 'I2P (.b32.i2p)', value: n.i2p, itemStyle: { color: '#38bdf8' } },
      { name: 'IPv6 Clearnet', value: n.ipv6, itemStyle: { color: '#22c55e' } },
      { name: 'CJDNS (fc00::/8)', value: n.cjdns, itemStyle: { color: '#f43f5e' } },
    ].filter(d => d.value > 0);
  }, [data]);

  if (miningPoll.error && !data) {
    return <ErrorBanner message={miningPoll.error} />;
  }
  if (miningPoll.loading && !data) {
    return <EmptyState text="Loading Bitcoin mining, pools, and node distribution…" />;
  }
  if (!data) {
    return <EmptyState text="No mining telemetry available." />;
  }

  const defaultPrice = data.price_usd ?? 0;
  const effectivePrice =
    priceOverride.trim() !== ''
      ? Math.max(0, parseFloat(priceOverride) || 0)
      : defaultPrice;
  const elec = Math.max(0, parseFloat(elecStr) || 0);
  const assumptions: Assumptions = {
    pue: Math.max(1, parseFloat(pueStr) || 1.05),
    poolFeePct: Math.min(99, Math.max(0, parseFloat(poolFeeStr) || 0)),
  };
  const customJth = Math.max(0, parseFloat(customJthStr) || 0);
  const tableRigs = [
    ...RIGS,
    ...(customJth > 0 ? [{ name: 'Custom rig', jth: customJth, color: '#ef4444' }] : []),
  ];

  const nowBtcPerPH = data.hashprice_now?.btc_per_ph_day ?? 0;
  const newestRig = RIGS[RIGS.length - 1];
  const satsPerThDay = btcPerThDay(nowBtcPerPH, assumptions.poolFeePct) * 1e8;

  const latestChain =
    data.chain && data.chain.length > 0 ? data.chain[data.chain.length - 1] : null;
  const hashrate = data.hashrate ?? [];
  const hashprice = data.hashprice ?? [];
  const pools = data.pools ?? [];
  const sources = data.sources ?? [];

  return (
    <div className="space-y-6">
      {/* Top Toolbar */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <div className="inline-flex rounded-lg border border-zinc-800 bg-zinc-900/80 p-0.5 text-xs">
            {PERIODS.map(p => (
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
          <span className="text-xs text-zinc-400">
            Hash Ribbon:{' '}
            <strong
              className={
                data.ribbon.state === 'capitulation'
                  ? 'text-rose-400'
                  : data.ribbon.state === 'recovery'
                    ? 'text-emerald-400'
                    : 'text-zinc-200'
              }
            >
              {data.ribbon.state.toUpperCase()}
            </strong>
            {data.ribbon.last_cross_up_days != null && (
              <span className="ml-1 text-zinc-500">
                (recovery cross {data.ribbon.last_cross_up_days}d ago)
              </span>
            )}
          </span>
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          {sources.map((s) => (
            <SourceBadge key={s.id} source={s} />
          ))}
        </div>
      </div>

      {/* Headline KPIs (§5.7) */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-6">
        <MetricCard
          label="Hashrate (7d)"
          value={
            data.current_hashrate_ehs != null
              ? `${fmtNum(data.current_hashrate_ehs)} EH/s`
              : '—'
          }
          icon={<Cpu size={15} />}
          detail={
            latestChain && bqControl.show
              ? `Implied ${fmtNum(latestChain.implied_hashrate_ehs)} EH/s (${latestChain.day})`
              : `Ribbon: ${data.ribbon.state}`
          }
          accentColor={BTC_COLOR}
        />
        <MetricCard
          label="Difficulty & Next Retarget"
          value={
            data.current_difficulty != null
              ? `${(data.current_difficulty / 1e12).toFixed(2)} T`
              : '—'
          }
          icon={<Pickaxe size={15} />}
          detail={
            data.difficulty
              ? `Est ${data.difficulty.est_change_pct >= 0 ? '+' : ''}${data.difficulty.est_change_pct.toFixed(2)}% in ${data.difficulty.remaining_blocks} blks`
              : '—'
          }
          accentColor="#60a5fa"
        />
        <MetricCard
          label="Hashprice (per PH/day)"
          value={
            data.hashprice_now?.usd_per_ph_day != null
              ? `$${data.hashprice_now.usd_per_ph_day.toFixed(2)}`
              : '—'
          }
          icon={<Zap size={15} />}
          detail={
            data.hashprice_now
              ? `${data.hashprice_now.btc_per_ph_day.toFixed(6)} BTC/PH/d (${satsPerThDay.toFixed(1)} sats/TH/d)`
              : '—'
          }
          accentColor="#22c55e"
        />
        <MetricCard
          label="Fee Share (144 Blocks)"
          value={data.fee_share_pct != null ? fmtPct(data.fee_share_pct, 2) : '—'}
          icon={<Pickaxe size={15} />}
          detail={
            data.rewards_144
              ? `${data.rewards_144.total_fee_btc.toFixed(2)} / ${data.rewards_144.total_reward_btc.toFixed(2)} BTC`
              : 'Last ~24h blocks'
          }
          accentColor="#f59e0b"
        />
        <MetricCard
          label="Security Budget"
          value={
            data.security_budget.annualized_revenue_usd != null
              ? fmtUSD(data.security_budget.annualized_revenue_usd)
              : data.security_budget.daily_miner_revenue_btc != null
                ? `${data.security_budget.daily_miner_revenue_btc.toFixed(1)} BTC/d`
                : '—'
          }
          icon={<Shield size={15} />}
          detail={
            data.security_budget.security_budget_pct != null
              ? `${data.security_budget.security_budget_pct.toFixed(2)}% of market cap / yr`
              : '—'
          }
          accentColor="#a855f7"
        />
        <MetricCard
          label="Halving Countdown"
          value={
            data.halving.blocks_remaining != null
              ? `${data.halving.blocks_remaining.toLocaleString()} blks`
              : '—'
          }
          icon={<Globe2 size={15} />}
          detail={
            data.halving.subsidy_now_btc != null &&
            data.halving.subsidy_next_btc != null &&
            data.halving.next_height != null
              ? `${data.halving.subsidy_now_btc} → ${data.halving.subsidy_next_btc} BTC (#${data.halving.next_height.toLocaleString()})`
              : '—'
          }
          accentColor="#38bdf8"
        />
      </div>

      {/* Hashrate & Ribbons + Hashprice History */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              <span>Network Hashrate &amp; Hash Ribbons (SMA30 / SMA60)</span>
              <HelpTip label="Hash Ribbons">
                Daily estimated hashrate (EH/s) with 30-day and 60-day simple moving averages. SMA30 &lt; SMA60 indicates miner capitulation; a cross back above SMA60 marks recovery (§10.2).
              </HelpTip>
            </span>
          }
          note={
            bqControl.show && latestChain?.hashrate_delta_pct != null
              ? `Latest BigQuery A2 Δ vs mempool: ${latestChain.hashrate_delta_pct >= 0 ? '+' : ''}${latestChain.hashrate_delta_pct.toFixed(2)}%`
              : `State: ${data.ribbon.state}`
          }
        >
          <ReactECharts
            style={{ height: 280 }}
            option={{
              tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
              legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
              grid: { left: 56, right: 24, top: 32, bottom: 24 },
              xAxis: {
                type: 'category',
                data: hashrate.map(p => p.date),
                axisLabel: AXIS_LABEL,
              },
              yAxis: {
                type: 'value',
                axisLabel: { ...AXIS_LABEL, formatter: '{value} EH' },
                splitLine: SPLIT_LINE,
              },
              series: [
                {
                  name: 'Hashrate (EH/s)',
                  type: 'line',
                  showSymbol: false,
                  data: hashrate.map(p => Number(p.hashrate_ehs.toFixed(2))),
                  lineStyle: { color: '#71717a', width: 1.2 },
                  itemStyle: { color: '#71717a' },
                },
                {
                  name: 'SMA30',
                  type: 'line',
                  showSymbol: false,
                  data: hashrate.map(p =>
                    p.sma30_ehs != null ? Number(p.sma30_ehs.toFixed(2)) : null,
                  ),
                  lineStyle: { color: '#f59e0b', width: 2 },
                  itemStyle: { color: '#f59e0b' },
                },
                {
                  name: 'SMA60',
                  type: 'line',
                  showSymbol: false,
                  data: hashrate.map(p =>
                    p.sma60_ehs != null ? Number(p.sma60_ehs.toFixed(2)) : null,
                  ),
                  lineStyle: { color: '#38bdf8', width: 2 },
                  itemStyle: { color: '#38bdf8' },
                },
                ...(bqControl.show && hashrate.some(p => p.implied_hashrate_ehs != null)
                  ? [
                      {
                        name: 'Implied Hashrate (BigQuery A2)',
                        type: 'line',
                        showSymbol: false,
                        data: hashrate.map(p =>
                          p.implied_hashrate_ehs != null
                            ? Number(p.implied_hashrate_ehs.toFixed(2))
                            : null,
                        ),
                        lineStyle: { color: '#22c55e', width: 1.5, type: 'dashed' },
                        itemStyle: { color: '#22c55e' },
                      },
                    ]
                  : []),
              ],
            }}
          />
        </Panel>

        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              <span>Hashprice History (USD &amp; BTC per PH/s/day)</span>
              <HelpTip label="Hashprice">
                Daily miner revenue per PH/s/day: reward_per_block × 86,400 × 10¹⁵ / (difficulty × 2³²) × price (§10.2).
              </HelpTip>
            </span>
          }
          note={
            data.hashprice_now
              ? `Now: ${data.hashprice_now.btc_per_ph_day.toFixed(6)} BTC/PH/d`
              : undefined
          }
        >
          <ReactECharts
            style={{ height: 280 }}
            option={{
              tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
              legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
              grid: { left: 56, right: 64, top: 32, bottom: 24 },
              xAxis: {
                type: 'category',
                data: hashprice.map(p => p.date),
                axisLabel: AXIS_LABEL,
              },
              yAxis: [
                {
                  type: 'value',
                  axisLabel: { ...AXIS_LABEL, formatter: '${value}' },
                  splitLine: SPLIT_LINE,
                },
                {
                  type: 'value',
                  axisLabel: { ...AXIS_LABEL, formatter: '{value} BTC' },
                  splitLine: { show: false },
                },
              ],
              series: [
                {
                  name: 'USD / PH / day',
                  type: 'line',
                  yAxisIndex: 0,
                  showSymbol: false,
                  data: hashprice.map(p =>
                    p.usd_per_ph_day != null ? Number(p.usd_per_ph_day.toFixed(2)) : null,
                  ),
                  lineStyle: { color: '#22c55e', width: 2 },
                  itemStyle: { color: '#22c55e' },
                },
                {
                  name: 'BTC / PH / day',
                  type: 'line',
                  yAxisIndex: 1,
                  showSymbol: false,
                  data: hashprice.map(p => Number(p.btc_per_ph_day.toFixed(6))),
                  lineStyle: { color: BTC_COLOR, width: 1.8 },
                  itemStyle: { color: BTC_COLOR },
                },
              ],
            }}
          />
        </Panel>
      </div>

      {/* Mining Pools & Block Production Concentration (§5.7, Principle 10) */}
      <Panel
        title={
          <span className="inline-flex items-center gap-1.5">
            <span>Mining Pools &amp; Block Production Concentration (1w)</span>
            <HelpTip label="Pool Concentration">
              Block attribution over the trailing 1-week window. Pool Nakamoto is the minimum number of pools exceeding 50% of blocks (Unknown excluded); HHI is Σ(share%²) with Unknown in the denominator only (§10.2).
            </HelpTip>
          </span>
        }
        note={`${data.concentration.total_blocks.toLocaleString()} blocks · ${data.concentration.known_blocks.toLocaleString()} attributed`}
      >
        <div className="space-y-4">
          {/* Separate row for Pool Concentration metrics (Principle 10) */}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <ConcentrationChip
              label="Pool Nakamoto Coefficient"
              metric={data.concentration.nakamoto}
              formatValue={v => `${Math.round(v)} pools`}
            />
            <ConcentrationChip
              label="Pool Herfindahl-Hirschman Index (HHI)"
              metric={data.concentration.hhi}
              formatValue={v => Math.round(v).toLocaleString()}
            />
          </div>

          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="border-b border-zinc-800/60 text-zinc-500">
                  <th className="py-2 text-left font-medium">#</th>
                  <th className="py-2 text-left font-medium">Pool</th>
                  <th className="py-2 text-right font-medium">Blocks (1w)</th>
                  <th className="py-2 text-right font-medium">Share</th>
                  <th className="py-2 text-right font-medium">Empty Blocks</th>
                  <th className="py-2 text-right font-medium">Template Match Rate</th>
                </tr>
              </thead>
              <tbody>
                {pools.map(p => (
                  <tr key={p.slug || p.name} className="border-b border-zinc-800/30">
                    <td className="py-2 font-mono text-zinc-500">{p.rank}</td>
                    <td className="py-2 font-medium text-zinc-200">{p.name}</td>
                    <td className="py-2 text-right font-mono text-zinc-300">
                      {p.block_count.toLocaleString()}
                    </td>
                    <td className="py-2 text-right font-mono text-amber-300">
                      {fmtPct(p.share_pct, 1)}
                    </td>
                    <td className="py-2 text-right font-mono text-zinc-400">
                      {p.empty_blocks}
                    </td>
                    <td className="py-2 text-right font-mono text-zinc-400">
                      {p.avg_match_rate > 0 ? `${p.avg_match_rate.toFixed(1)}%` : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </Panel>

      {/* Node Distribution (Bitnodes + DB-IP Lite, §5.7, Principle 10) */}
      <Panel
        title={
          <span className="inline-flex items-center gap-1.5">
            <span>Reachable Node Distribution &amp; Hosting Topology</span>
            <HelpTip label="Node Distribution">
              Reachable listening nodes from Bitnodes (refreshed ≤ 4×/day and persisted in SQLite), joined locally against monthly DB-IP Lite country and ASN ranges over clearnet (IPv4 + IPv6) nodes (§5.7, §10.2).
            </HelpTip>
          </span>
        }
        note={
          data.nodes
            ? `${data.nodes.reachable.toLocaleString()} reachable · ${data.nodes.clearnet.toLocaleString()} clearnet · as of ${data.nodes.as_of.slice(0, 16).replace('T', ' ')} UTC`
            : 'Snapshot unavailable'
        }
      >
        {data.nodes ? (
          <div className="space-y-5">
            {/* Separate row for Node Concentration metrics (Principle 10: never merged or ranked against pools) */}
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <ConcentrationChip
                label="Country Nakamoto (Clearnet)"
                metric={data.nodes.country_nakamoto}
                formatValue={v => `${Math.round(v)} countries`}
              />
              <ConcentrationChip
                label="ASN Nakamoto (Clearnet)"
                metric={data.nodes.asn_nakamoto}
                formatValue={v => `${Math.round(v)} ASNs`}
              />
              <ConcentrationChip
                label="ASN HHI (Clearnet)"
                metric={data.nodes.asn_hhi}
                formatValue={v => Math.round(v).toLocaleString()}
              />
              <ConcentrationChip
                label="Cloud / Hosting Share"
                metric={data.nodes.cloud_share}
                formatValue={v => fmtPct(v, 1)}
              />
            </div>

            {/* Network Mix Donut + Client & Core Version Bars */}
            <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
              <div className="rounded-lg border border-zinc-800/60 bg-zinc-950/40 p-3">
                <div className="mb-2 text-xs font-medium text-zinc-300">
                  Network Mix ({data.nodes.reachable.toLocaleString()} reachable)
                </div>
                <ReactECharts
                  style={{ height: 220 }}
                  option={{
                    tooltip: { trigger: 'item', ...CHART_TOOLTIP },
                    legend: {
                      bottom: 0,
                      textStyle: { color: '#a1a1aa', fontSize: 10 },
                    },
                    series: [
                      {
                        name: 'Network',
                        type: 'pie',
                        radius: ['45%', '72%'],
                        center: ['50%', '44%'],
                        label: { show: false },
                        data: networkMixData,
                      },
                    ],
                  }}
                />
              </div>

              <div className="rounded-lg border border-zinc-800/60 bg-zinc-950/40 p-3">
                <div className="mb-2 text-xs font-medium text-zinc-300">
                  Client Implementations (Core vs Knots)
                </div>
                <div className="space-y-2.5 pt-1">
                  {data.nodes.clients.map(c => (
                    <div key={c.label} className="space-y-1">
                      <div className="flex items-center justify-between text-xs">
                        <span className="text-zinc-200">{c.label}</span>
                        <span className="font-mono text-zinc-400">
                          {c.count.toLocaleString()} ({fmtPct(c.share_pct, 1)})
                        </span>
                      </div>
                      <div className="h-2 w-full overflow-hidden rounded-full bg-zinc-800">
                        <div
                          className="h-full rounded-full bg-amber-500"
                          style={{ width: `${Math.min(100, c.share_pct)}%` }}
                        />
                      </div>
                    </div>
                  ))}
                </div>
              </div>

              <div className="rounded-lg border border-zinc-800/60 bg-zinc-950/40 p-3">
                <div className="mb-2 text-xs font-medium text-zinc-300">
                  Bitcoin Core Major Versions
                </div>
                <div className="space-y-2 pt-1">
                  {data.nodes.core_versions.slice(0, 6).map(v => (
                    <div key={v.label} className="space-y-1">
                      <div className="flex items-center justify-between text-xs">
                        <span className="font-mono text-zinc-200">{v.label}</span>
                        <span className="font-mono text-zinc-400">
                          {v.count.toLocaleString()} ({fmtPct(v.share_pct, 1)})
                        </span>
                      </div>
                      <div className="h-1.5 w-full overflow-hidden rounded-full bg-zinc-800">
                        <div
                          className="h-full rounded-full bg-sky-400"
                          style={{ width: `${Math.min(100, v.share_pct)}%` }}
                        />
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            </div>

            {/* World Choropleth Map */}
            <NodeMap
              countries={data.nodes.countries}
              clearnetTotal={data.nodes.clearnet}
            />

            {/* Top Hosting ASNs Table */}
            <div className="overflow-x-auto">
              <div className="mb-2 flex items-center justify-between text-xs text-zinc-400">
                <span>
                  Top Hosting Autonomous Systems ({data.nodes.total_asns.toLocaleString()} ASNs across{' '}
                  {data.nodes.clearnet.toLocaleString()} clearnet nodes · {data.nodes.unresolved} unresolved)
                </span>
              </div>
              <table className="w-full text-xs">
                <thead>
                  <tr className="border-b border-zinc-800/60 text-zinc-500">
                    <th className="py-1.5 text-left font-medium">ASN</th>
                    <th className="py-1.5 text-left font-medium">Organization</th>
                    <th className="py-1.5 text-left font-medium">Classification</th>
                    <th className="py-1.5 text-right font-medium">Clearnet Nodes</th>
                    <th className="py-1.5 text-right font-medium">Share of Clearnet</th>
                  </tr>
                </thead>
                <tbody>
                  {data.nodes.asns.slice(0, 12).map(a => (
                    <tr key={a.asn} className="border-b border-zinc-800/30">
                      <td className="py-1.5 font-mono text-zinc-400">AS{a.asn}</td>
                      <td className="py-1.5 text-zinc-200">{a.org || '—'}</td>
                      <td className="py-1.5">
                        {a.is_cloud ? (
                          <span className="rounded bg-amber-500/15 px-1.5 py-0.5 text-[10px] font-medium text-amber-300">
                            Cloud / Datacenter
                          </span>
                        ) : (
                          <span className="text-[11px] text-zinc-500">ISP / Other</span>
                        )}
                      </td>
                      <td className="py-1.5 text-right font-mono text-zinc-300">
                        {a.count.toLocaleString()}
                      </td>
                      <td className="py-1.5 text-right font-mono text-amber-300">
                        {fmtPct(a.share_pct, 1)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            {/* Mandatory Caveats & Attribution (§5.7, §11.2) */}
            <div className="flex flex-col justify-between gap-2 rounded-lg border border-zinc-800/60 bg-zinc-900/40 px-3.5 py-2.5 text-[11px] text-zinc-400 sm:flex-row sm:items-center">
              <ul className="list-inside list-disc space-y-0.5">
                <li>Only reachable (listening) nodes are visible; private / non-listening full nodes are excluded.</li>
                <li>Tor (.onion) and I2P (.b32.i2p) nodes cannot be geolocated or mapped to an ASN.</li>
                <li>An ASN identifies a hosting network or ISP, not a single node operator.</li>
              </ul>
              <div className="shrink-0 text-[11px] text-zinc-500">
                {data.nodes.attribution}
              </div>
            </div>
          </div>
        ) : (
          <EmptyState text="Reachable node snapshot is not yet available." />
        )}
      </Panel>

      {/* BigQuery On-Chain Mining Verification (A2 Implied Hashrate & Issuance) */}
      {!bqControl.show ? (
        <BigQueryLocked
          title="BigQuery On-Chain Mining & Issuance Accounting (A2)"
          description="Daily exact block-header difficulty decode, implied hashrate Δ% vs mempool.space, empty block counts, and claimed vs scheduled subsidy verification."
          bigquery={bigquery}
          onEnableDisplay={bqControl.available ? bqControl.toggle : undefined}
        />
      ) : data.chain && data.chain.length > 0 ? (
        <Panel
          title={
            <span className="inline-flex items-center gap-2">
              <span>On-Chain Block Header &amp; Issuance Verification (BigQuery A2)</span>
              <HelpTip label="On-Chain Mining Verification">
                Implied hashrate = avg_difficulty × 2³² × blocks / 86,400 from BigQuery block headers, compared against mempool.space daily estimates, plus claimed coinbase vs scheduled subsidy (§7.2, §10.3).
              </HelpTip>
              <button
                type="button"
                onClick={() =>
                  downloadCsv(
                    `bitcoin-mining-onchain-${period}.csv`,
                    ['day', 'blocks', 'empty_blocks', 'implied_hashrate_ehs', 'mempool_hashrate_ehs', 'hashrate_delta_pct', 'scheduled_sats', 'claimed_sats', 'fees_sats', 'fee_share_pct'],
                    (data.chain ?? []).map(r => [
                      r.day,
                      r.blocks,
                      r.empty_blocks,
                      r.implied_hashrate_ehs.toFixed(2),
                      r.mempool_hashrate_ehs?.toFixed(2) ?? '',
                      r.hashrate_delta_pct?.toFixed(2) ?? '',
                      r.scheduled_sats,
                      r.claimed_sats,
                      r.fees_sats,
                      r.fee_share_pct.toFixed(3),
                    ]),
                  )
                }
                className="inline-flex items-center gap-1 rounded border border-zinc-800 bg-zinc-900/80 px-2 py-0.5 text-[11px] text-amber-400 hover:text-amber-300"
              >
                <Download className="h-3 w-3" />
                <span>CSV</span>
              </button>
            </span>
          }
          note={`${data.chain.length} UTC days stored`}
        >
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="border-b border-zinc-800/60 text-zinc-500">
                  <th className="py-1.5 text-left font-medium">UTC Day</th>
                  <th className="py-1.5 text-right font-medium">Blocks</th>
                  <th className="py-1.5 text-right font-medium">Empty</th>
                  <th className="py-1.5 text-right font-medium">Implied EH/s</th>
                  <th className="py-1.5 text-right font-medium">Mempool EH/s</th>
                  <th className="py-1.5 text-right font-medium">Δ%</th>
                  <th className="py-1.5 text-right font-medium">Scheduled Issuance</th>
                  <th className="py-1.5 text-right font-medium">Claimed Issuance</th>
                  <th className="py-1.5 text-right font-medium">Fee Share</th>
                </tr>
              </thead>
              <tbody>
                {data.chain.slice(-14).reverse().map(r => (
                  <tr key={r.day} className="border-b border-zinc-800/30">
                    <td className="py-1.5 font-mono text-zinc-300">{r.day}</td>
                    <td className="py-1.5 text-right font-mono text-zinc-300">{r.blocks}</td>
                    <td className="py-1.5 text-right font-mono text-zinc-400">{r.empty_blocks}</td>
                    <td className="py-1.5 text-right font-mono text-emerald-400">
                      {r.implied_hashrate_ehs.toFixed(1)}
                    </td>
                    <td className="py-1.5 text-right font-mono text-zinc-400">
                      {r.mempool_hashrate_ehs != null ? r.mempool_hashrate_ehs.toFixed(1) : '—'}
                    </td>
                    <td className="py-1.5 text-right font-mono text-zinc-300">
                      {r.hashrate_delta_pct != null
                        ? `${r.hashrate_delta_pct >= 0 ? '+' : ''}${r.hashrate_delta_pct.toFixed(2)}%`
                        : '—'}
                    </td>
                    <td className="py-1.5 text-right font-mono text-zinc-300">
                      {fmtBTCFromSats(r.scheduled_sats)}
                    </td>
                    <td className="py-1.5 text-right font-mono text-zinc-300">
                      {fmtBTCFromSats(r.claimed_sats)}
                    </td>
                    <td className="py-1.5 text-right font-mono text-amber-300">
                      {fmtPct(r.fee_share_pct, 2)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Panel>
      ) : (
        <EmptyState text="No BigQuery on-chain rollup rows stored for this window yet." />
      )}

      {/* Rig Economics Calculator (§5.7, Task 5.3) */}
      <Panel
        title={
          <span className="inline-flex items-center gap-1.5">
            <span>ASIC Rig Shutdown Price &amp; Production Cost Calculator</span>
            <HelpTip label="Rig Economics">
              Driven by API hashprice (BTC/PH/day): shutdown price ($/kWh) = net_btc_per_TH_day × price / (0.024 × J/TH × PUE). Opex-only; hardware capex and hosting fees are excluded.
            </HelpTip>
          </span>
        }
        note={`Shutdown price (${newestRig.jth} J/TH): ${
          effectivePrice > 0 && nowBtcPerPH > 0
            ? `$${breakEvenKwh(nowBtcPerPH, newestRig.jth, effectivePrice, assumptions).toFixed(3)}/kWh`
            : '—'
        }`}
      >
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-5">
            <NumberInput
              label="BTC price"
              unit="USD"
              value={priceOverride !== '' ? priceOverride : String(defaultPrice || '')}
              onChange={setPriceOverride}
              width="w-28"
            />
            <NumberInput
              label="Electricity"
              unit="$/kWh"
              value={elecStr}
              onChange={setElecStr}
              width="w-20"
            />
            <NumberInput
              label="PUE"
              unit="× energy"
              value={pueStr}
              onChange={setPueStr}
              width="w-20"
            />
            <NumberInput
              label="Pool fee"
              unit="%"
              value={poolFeeStr}
              onChange={setPoolFeeStr}
              width="w-16"
            />
            <NumberInput
              label="Custom rig"
              unit="J/TH"
              value={customJthStr}
              onChange={setCustomJthStr}
              width="w-20"
            />
          </div>

          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="border-b border-zinc-800/60 text-zinc-500">
                  <th className="py-2 text-left font-medium">Rig</th>
                  <th className="py-2 text-right font-medium">Efficiency</th>
                  <th className="py-2 text-right font-medium">Break-even ($/kWh)</th>
                  <th className="py-2 text-right font-medium">Cost to Mine 1 BTC</th>
                  <th className="py-2 text-right font-medium">Operating Margin</th>
                </tr>
              </thead>
              <tbody>
                {tableRigs.map(rig => {
                  const be = breakEvenKwh(nowBtcPerPH, rig.jth, effectivePrice, assumptions);
                  const cost = costPerBtc(nowBtcPerPH, rig.jth, elec, assumptions);
                  const margin =
                    effectivePrice > 0 && cost > 0
                      ? ((effectivePrice - cost) / effectivePrice) * 100
                      : 0;
                  const profitable = effectivePrice > 0 && be >= elec;
                  return (
                    <tr key={rig.name} className="border-b border-zinc-800/30">
                      <td className="py-2 text-zinc-200">
                        <span
                          className="mr-2 inline-block h-2 w-2 rounded-full"
                          style={{ background: rig.color }}
                        />
                        {rig.name}
                      </td>
                      <td className="py-2 text-right font-mono text-zinc-400">
                        {rig.jth} J/TH
                      </td>
                      <td
                        className="py-2 text-right font-mono"
                        style={{ color: profitable ? '#22c55e' : '#ef4444' }}
                      >
                        {effectivePrice > 0 && nowBtcPerPH > 0 ? `$${be.toFixed(3)}/kWh` : '—'}
                      </td>
                      <td className="py-2 text-right font-mono text-zinc-200">
                        {cost > 0 ? `$${fmtNum(cost)}` : '—'}
                      </td>
                      <td
                        className="py-2 text-right font-mono"
                        style={{ color: margin >= 0 ? '#22c55e' : '#ef4444' }}
                      >
                        {effectivePrice > 0 && cost > 0
                          ? `${margin >= 0 ? '+' : ''}${margin.toFixed(1)}%`
                          : '—'}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      </Panel>
    </div>
  );
}

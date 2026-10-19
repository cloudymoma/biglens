import { useMemo } from 'react';
import ReactECharts from 'echarts-for-react';
import {
  Activity,
  ArrowDownUp,
  CheckCircle2,
  Coins,
  Cpu,
  Download,
  Gauge,
  Layers,
  Scale,
  Shield,
  TrendingDown,
  TrendingUp,
} from 'lucide-react';
import type {
  BtcMetaBigQuery,
  BtcOverviewData,
  BtcRegimeSignal,
  BtcVerificationRow,
} from './types';
import type { BitcoinBigQueryControl } from './useBitcoinBigQuery';
import { fetchBitcoinOverview } from './api';
import { usePolling } from './usePolling';
import SourceBadge from './SourceBadge';
import BigQueryLocked from './BigQueryLocked';
import { EmptyState, ErrorBanner, MetricCard } from '../../dashboards/shared';
import {
  AXIS_LABEL,
  BTC_COLOR,
  CHART_TOOLTIP,
  ETH_COLOR,
  Panel,
  SPLIT_LINE,
} from '../crypto/shared';
import HelpTip from '../crypto/HelpTip';
import { downloadCsv, fmtPct, fmtUSD } from './format';

interface Props {
  bqControl: BitcoinBigQueryControl;
  bigquery: BtcMetaBigQuery | null;
  refreshTick: number;
}

const fmtInt = (n: number): string => Math.round(n).toLocaleString();

function stateBadgeClass(state: string): string {
  switch (state) {
    case 'above_both':
    case 'recovery':
    case 'window_open':
    case 'net_outflow':
    case 'cheap':
    case 'low':
      return 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300';
    case 'between':
    case 'normal':
    case 'neutral':
    case 'fair':
    case 'mid':
      return 'border-sky-500/30 bg-sky-500/10 text-sky-300';
    case 'elevated':
    case 'rich':
    case 'high':
    case 'closed':
    case 'net_inflow':
      return 'border-amber-500/30 bg-amber-500/10 text-amber-300';
    case 'below_both':
    case 'capitulation':
    case 'overheated':
    case 'negative':
      return 'border-rose-500/30 bg-rose-500/10 text-rose-300';
    default:
      return 'border-zinc-700 bg-zinc-800/60 text-zinc-400';
  }
}

function formatStateLabel(state: string): string {
  return state.replace(/_/g, ' ');
}

function formatVerificationValue(v: number | null, unit: string): string {
  if (v == null || !Number.isFinite(v)) return '—';
  switch (unit) {
    case 'EH/s':
      return `${v.toFixed(2)} EH/s`;
    case 'txs':
    case 'addrs':
      return fmtInt(v);
    case 'BTC':
      return `${v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })} BTC`;
    case 'ratio':
      return `${v.toFixed(2)}×`;
    default:
      return v.toFixed(2);
  }
}

export default function OverviewTab({ bqControl, bigquery, refreshTick }: Props) {
  const { data, loading, error } = usePolling<BtcOverviewData>(
    fetchBitcoinOverview,
    0,
    `btc-overview-${refreshTick}`,
    true,
  );

  const priceOption = useMemo(() => {
    const pts = data?.price_chart ?? [];
    if (pts.length === 0) return null;
    const dates = pts.map((p) => p.date);
    const closes = pts.map((p) => p.close_usd);
    const sma200 = pts.map((p) => p.sma200 ?? null);
    const wma200 = pts.map((p) => p.wma200 ?? null);
    const realized = pts.map((p) => p.realized_price_usd ?? null);
    const hasRealized = bqControl.show && realized.some((v) => v != null && v > 0);

    const series: Record<string, unknown>[] = [
      {
        name: 'Close (USD)',
        type: 'line',
        showSymbol: false,
        data: closes,
        lineStyle: { width: 2, color: BTC_COLOR },
        itemStyle: { color: BTC_COLOR },
      },
      {
        name: '200DMA',
        type: 'line',
        showSymbol: false,
        data: sma200,
        lineStyle: { width: 1.5, color: ETH_COLOR },
        itemStyle: { color: ETH_COLOR },
      },
      {
        name: '200WMA (1,400d)',
        type: 'line',
        showSymbol: false,
        data: wma200,
        lineStyle: { width: 1.5, type: 'dashed', color: '#10b981' },
        itemStyle: { color: '#10b981' },
      },
    ];

    if (hasRealized) {
      series.push({
        name: 'Realized Price (BigQuery)',
        type: 'line',
        showSymbol: false,
        data: realized,
        lineStyle: { width: 1.75, color: '#a855f7' },
        itemStyle: { color: '#a855f7' },
      });
    }

    return {
      backgroundColor: 'transparent',
      tooltip: {
        ...CHART_TOOLTIP,
        trigger: 'axis',
      },
      legend: {
        top: 0,
        textStyle: { color: '#a1a1aa', fontSize: 11 },
      },
      grid: {
        left: 56,
        right: 16,
        top: 32,
        bottom: 28,
      },
      xAxis: {
        type: 'category',
        data: dates,
        axisLine: { lineStyle: { color: '#27272a' } },
        axisLabel: AXIS_LABEL,
      },
      yAxis: {
        type: 'log',
        logBase: 10,
        scale: true,
        axisLine: { lineStyle: { color: '#27272a' } },
        splitLine: SPLIT_LINE,
        axisLabel: {
          ...AXIS_LABEL,
          formatter: (v: number) => `$${fmtInt(v)}`,
        },
      },
      series,
    };
  }, [data, bqControl.show]);

  const fngOption = useMemo(() => {
    const pts = data?.sentiment.fear_greed_history ?? [];
    if (pts.length === 0) return null;
    return {
      backgroundColor: 'transparent',
      tooltip: {
        ...CHART_TOOLTIP,
        trigger: 'axis',
      },
      grid: { left: 36, right: 12, top: 12, bottom: 24 },
      xAxis: {
        type: 'category',
        data: pts.map((p) => p.date),
        axisLine: { lineStyle: { color: '#27272a' } },
        axisLabel: AXIS_LABEL,
      },
      yAxis: {
        type: 'value',
        min: 0,
        max: 100,
        axisLine: { lineStyle: { color: '#27272a' } },
        splitLine: SPLIT_LINE,
        axisLabel: AXIS_LABEL,
      },
      series: [
        {
          name: 'Fear & Greed',
          type: 'line',
          showSymbol: false,
          data: pts.map((p) => p.value),
          lineStyle: { width: 2, color: BTC_COLOR },
          itemStyle: { color: BTC_COLOR },
          areaStyle: { color: 'rgba(247, 147, 26, 0.14)' },
        },
      ],
    };
  }, [data]);

  if (error && !data) return <ErrorBanner message={error} />;
  if (loading && !data) return <EmptyState text="Loading Bitcoin overview…" />;
  if (!data) return <EmptyState text="No Bitcoin overview data available" />;

  const { cards, signals, supply, sentiment, verification } = data;
  const conc = cards.concentration;

  return (
    <div className="space-y-4">
      {/* Source badges bar */}
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-zinc-800/70 bg-zinc-900/40 px-3 py-2">
        <span className="text-xs text-zinc-400">
          Executive snapshot across market structure, valuation, holder flows, fee market & network security
        </span>
        <div className="flex flex-wrap items-center gap-1.5">
          {data.sources.map((s) => (
            <SourceBadge key={`${s.id}-${s.via ?? ''}`} source={s} />
          ))}
        </div>
      </div>

      {/* Headline KPI Row 1 (5 cards, §5.0 / §5.2) */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <MetricCard
          label="Market Cap · Dominance"
          value={cards.market_cap_usd != null ? fmtUSD(cards.market_cap_usd) : '—'}
          icon={<Coins size={15} />}
          detail={
            cards.dominance_pct != null
              ? `BTC dominance: ${fmtPct(cards.dominance_pct)}`
              : 'Dominance unavailable'
          }
          accentColor={BTC_COLOR}
        />

        <MetricCard
          label="Drawdown from ATH"
          value={cards.drawdown_pct != null ? fmtPct(cards.drawdown_pct, 2, true) : '—'}
          icon={<TrendingDown size={15} />}
          detail={
            cards.ath_usd != null
              ? `ATH $${fmtInt(cards.ath_usd)}${cards.ath_date ? ` (${cards.ath_date})` : ''}`
              : 'ATH unavailable'
          }
          accentColor="#f43f5e"
        />

        <MetricCard
          label="RV30 vs DVOL"
          value={
            cards.rv30 != null && cards.dvol != null
              ? `${cards.rv30.toFixed(1)}% / ${cards.dvol.toFixed(1)}%`
              : cards.rv30 != null
                ? `RV30 ${cards.rv30.toFixed(1)}%`
                : '—'
          }
          icon={<Activity size={15} />}
          detail={
            cards.vrp != null
              ? `VRP (DVOL − RV30): ${cards.vrp >= 0 ? '+' : ''}${cards.vrp.toFixed(1)} pts`
              : 'Deribit DVOL − 30d realized vol'
          }
          accentColor={ETH_COLOR}
        />

        <MetricCard
          label="Funding APR · Total OI"
          value={
            cards.funding_oi_weighted_apr != null
              ? `${cards.funding_oi_weighted_apr >= 0 ? '+' : ''}${cards.funding_oi_weighted_apr.toFixed(2)}% APR`
              : '—'
          }
          icon={<TrendingUp size={15} />}
          detail={
            cards.total_oi_usd != null
              ? `Curated OI: ${fmtUSD(cards.total_oi_usd)}`
              : 'OI-weighted direct perp funding'
          }
          accentColor="#10b981"
        />

        <MetricCard
          label="Deriv / Spot Ratio"
          value={cards.major_deriv_spot != null ? `${cards.major_deriv_spot.toFixed(2)}×` : '—'}
          icon={<Scale size={15} />}
          detail={
            cards.all_reported_deriv_spot != null
              ? `Curated venues · all-reported ${cards.all_reported_deriv_spot.toFixed(1)}× (unverified)`
              : 'Curated major venues 24h volume'
          }
          accentColor="#38bdf8"
        />
      </div>

      {/* Headline KPI Row 2 (4 standard cards + 1 two-row Concentration card, §5.2 / §10.2) */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <MetricCard
          label="Mayer Multiple · Pctl"
          value={cards.mayer != null ? `${cards.mayer.toFixed(2)}×` : '—'}
          icon={<Gauge size={15} />}
          detail={
            cards.mayer_pctl_since_2011 != null
              ? `${cards.mayer_pctl_since_2011.toFixed(1)}th pctl since 2011 (Close / 200DMA)`
              : 'Close / 200-day SMA'
          }
          accentColor={BTC_COLOR}
        />

        <MetricCard
          label="MVRV · Percentile"
          value={cards.cm_mvrv != null ? `${cards.cm_mvrv.toFixed(2)}×` : '—'}
          icon={<Layers size={15} />}
          detail={
            bqControl.show && cards.biglens_mvrv != null
              ? `BigLens ${cards.biglens_mvrv.toFixed(2)}× (${cards.mvrv_delta_pct != null ? fmtPct(cards.mvrv_delta_pct, 2, true) : '—'})`
              : cards.cm_mvrv_pctl_since_2011 != null
                ? `Coin Metrics ${cards.cm_mvrv_pctl_since_2011.toFixed(1)}th pctl since 2011`
                : 'Coin Metrics CapMVRVCur'
          }
          accentColor="#a855f7"
        />

        <MetricCard
          label="Hashprice (PH/s/day)"
          value={
            cards.hashprice_usd_per_ph_day != null
              ? `$${cards.hashprice_usd_per_ph_day.toFixed(2)}`
              : '—'
          }
          icon={<Cpu size={15} />}
          detail={
            cards.hashprice_btc_per_ph_day != null
              ? `${cards.hashprice_btc_per_ph_day.toFixed(6)} BTC / PH / day`
              : 'Miner revenue per PH/s/day'
          }
          accentColor="#eab308"
        />

        <MetricCard
          label="Exchange Net Flow (7d)"
          value={
            cards.exchange_net_flow_7d_usd != null
              ? `${cards.exchange_net_flow_7d_usd > 0 ? '+' : ''}${fmtUSD(cards.exchange_net_flow_7d_usd)}`
              : '—'
          }
          icon={<ArrowDownUp size={15} />}
          detail={
            cards.exchange_net_flow_7d_usd != null
              ? `${cards.exchange_net_flow_7d_usd < 0 ? 'Net outflow (withdrawal)' : 'Net inflow (deposit)'}${cards.exchange_flow_flash ? ' · flash (provisional)' : ''}`
              : 'Coin Metrics 7d FlowInExUSD − FlowOutExUSD'
          }
          accentColor="#14b8a6"
        />

        {/* Two-row Concentration card (§5.2, §10.2, Principle 10: separate rows, never merged or compared) */}
        <div
          className="flex flex-col justify-between rounded-2xl border border-zinc-800/80 p-3.5"
          style={{ background: '#111114' }}
        >
          <div className="flex items-center justify-between gap-1 text-xs font-medium text-zinc-400">
            <span className="inline-flex items-center gap-1">
              Concentration: Pools · Nodes
              <HelpTip label="Concentration (Separate Layers)">
                Mining pool attribution and reachable node geography/hosting measure different layers with different
                denominators and biases. They are presented in separate rows with their own denominator and method and
                are never merged into a single score.
              </HelpTip>
            </span>
            <Shield size={14} className="text-zinc-500" />
          </div>

          <div className="mt-2 space-y-2 text-[11px]">
            <div className="rounded-lg border border-zinc-800/70 bg-zinc-900/50 px-2.5 py-1.5">
              <div className="flex items-center justify-between text-zinc-200">
                <span className="font-semibold">Pools ({conc.pool_period || '1w'})</span>
                <span className="font-mono">
                  Nakamoto {conc.pool_nakamoto.value || '—'} · HHI {Math.round(conc.pool_hhi.value) || '—'}
                </span>
              </div>
              <div className="text-[10px] text-zinc-500">
                n={fmtInt(conc.pool_nakamoto.denominator)} {conc.pool_nakamoto.unit || 'blocks'} ·{' '}
                {conc.pool_nakamoto.coverage_pct.toFixed(1)}% attributed (Unknown excl.)
              </div>
            </div>

            <div className="rounded-lg border border-zinc-800/70 bg-zinc-900/50 px-2.5 py-1.5">
              <div className="flex items-center justify-between text-zinc-200">
                <span className="font-semibold">Clearnet Nodes</span>
                <span className="font-mono">
                  {conc.node_country_nakamoto
                    ? `Country Nak. ${conc.node_country_nakamoto.value} · ASN Nak. ${conc.node_asn_nakamoto?.value ?? '—'}`
                    : '—'}
                </span>
              </div>
              <div className="text-[10px] text-zinc-500">
                {conc.node_clearnet > 0 ? (
                  <>
                    n={fmtInt(conc.node_clearnet)} clearnet ({conc.node_clearnet_share_pct?.toFixed(1) ?? '—'}% of{' '}
                    {fmtInt(conc.node_reachable)} reachable · DB-IP)
                  </>
                ) : (
                  'Bitnodes / DB-IP snapshot unavailable'
                )}
              </div>
            </div>
          </div>
        </div>
      </div>

      {/* Price Chart (1y log) + 7 Regime Indicators (§5.2, §10.4) */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <Panel
            title={
              <span className="inline-flex items-center gap-1.5">
                Price (1y Log) · 200DMA · 200WMA
                {bqControl.show ? ' · Realized Price' : ''}
                <HelpTip label="1y Log Price & Long-Term Moving Averages">
                  Daily BTC-USD close on a logarithmic scale alongside the 200-day simple moving average (200DMA), the
                  200-week moving average (1,400-day SMA), and—when BigQuery panels are enabled and a UTXO baseline
                  exists—the on-chain Realized Price (Σ UTXO value at creation price ÷ supply).
                </HelpTip>
              </span>
            }
            note={
              cards.realized_price_blocked && bqControl.show
                ? `Realized price status: ${cards.realized_price_blocked.replace(/_/g, ' ')}`
                : '1-year daily close (log scale)'
            }
          >
            {priceOption ? (
              <ReactECharts option={priceOption} style={{ height: 320 }} notMerge />
            ) : (
              <EmptyState text="Price series warming up" />
            )}
          </Panel>
        </div>

        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              Regime Indicators (7 Rules)
              <HelpTip label="Descriptive Regime Indicators (§10.4)">
                Seven transparent, rule-based indicators summarizing trend, valuation percentile, miner hash ribbon,
                derivatives leverage, implied-vs-realized volatility, UTXO consolidation fee window, and 7-day exchange
                net flows.
              </HelpTip>
            </span>
          }
          note="Descriptive rules with fixed thresholds"
        >
          <div className="space-y-2">
            {signals.map((sig: BtcRegimeSignal) => (
              <div
                key={sig.id}
                className="flex items-start justify-between gap-2 rounded-xl border border-zinc-800/70 bg-zinc-900/40 px-3 py-2"
              >
                <div className="min-w-0">
                  <div className="flex items-center gap-1 text-xs font-medium text-zinc-200">
                    <span>{sig.label}</span>
                    <HelpTip label={`${sig.label} Rule`}>{sig.rule}</HelpTip>
                  </div>
                  <div className="mt-0.5 truncate text-[11px] text-zinc-400" title={sig.summary}>
                    {sig.summary}
                  </div>
                </div>
                <span
                  className={`shrink-0 rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${stateBadgeClass(sig.state)}`}
                >
                  {formatStateLabel(sig.state)}
                </span>
              </div>
            ))}
          </div>
        </Panel>
      </div>

      {/* Supply & Issuance + Sentiment (Fear & Greed) (§5.2) */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              Supply, Issuance & Custodial Footprint
              <HelpTip label="Scheduled Supply & Custodial Balances">
                Scheduled supply is derived deterministically from block height (∑ 210,000 × 50×10⁸ ≫ epoch) and
                compared against the 21M cap, Coin Metrics tracked exchange balances, and CoinGecko public corporate
                treasuries.
              </HelpTip>
            </span>
          }
          note={
            supply.tip_height != null && supply.subsidy_btc != null
              ? `Tip #${fmtInt(supply.tip_height)} · subsidy ${supply.subsidy_btc} BTC/block`
              : 'Tip — · subsidy —'
          }
        >
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div className="rounded-xl border border-zinc-800/70 bg-zinc-900/40 p-3">
              <div className="text-[11px] text-zinc-400">Scheduled Supply</div>
              <div className="mt-1 font-mono text-sm font-semibold text-white">
                {supply.scheduled_supply_btc != null
                  ? `${fmtInt(supply.scheduled_supply_btc)} BTC`
                  : '—'}
              </div>
              <div className="mt-0.5 text-[11px] text-amber-300">
                {supply.mined_pct != null ? `${supply.mined_pct.toFixed(2)}% of 21M` : '—'}
              </div>
            </div>

            <div className="rounded-xl border border-zinc-800/70 bg-zinc-900/40 p-3">
              <div className="text-[11px] text-zinc-400">Annual Issuance</div>
              <div className="mt-1 font-mono text-sm font-semibold text-white">
                {supply.annual_issuance_btc != null
                  ? `${fmtInt(supply.annual_issuance_btc)} BTC/yr`
                  : '—'}
              </div>
              <div className="mt-0.5 text-[11px] text-zinc-400">
                {supply.annual_inflation_pct != null
                  ? `${supply.annual_inflation_pct.toFixed(2)}% annualized inflation`
                  : '—'}
              </div>
            </div>

            <div className="rounded-xl border border-zinc-800/70 bg-zinc-900/40 p-3">
              <div className="text-[11px] text-zinc-400">Exchange Balance (CM)</div>
              <div className="mt-1 font-mono text-sm font-semibold text-white">
                {supply.exchange_balance_btc != null
                  ? `${fmtInt(supply.exchange_balance_btc)} BTC`
                  : '—'}
              </div>
              <div className="mt-0.5 text-[11px] text-zinc-400">
                {supply.exchange_balance_pct != null
                  ? `${supply.exchange_balance_pct.toFixed(2)}% of supply`
                  : 'Coin Metrics SplyExNtv'}
              </div>
            </div>

            <div className="rounded-xl border border-zinc-800/70 bg-zinc-900/40 p-3">
              <div className="text-[11px] text-zinc-400">Public Treasuries</div>
              <div className="mt-1 font-mono text-sm font-semibold text-white">
                {supply.treasuries_btc != null
                  ? `${fmtInt(supply.treasuries_btc)} BTC`
                  : '—'}
              </div>
              <div className="mt-0.5 text-[11px] text-zinc-400">
                {supply.treasuries_companies > 0
                  ? `${supply.treasuries_companies} public companies (${supply.treasuries_pct?.toFixed(2) ?? '—'}% of 21M)`
                  : 'CoinGecko public treasuries'}
              </div>
            </div>
          </div>
        </Panel>

        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              Market Sentiment (Fear & Greed Index)
              <HelpTip label="Fear & Greed Index">
                Daily crypto market sentiment index (0 = Extreme Fear, 100 = Extreme Greed) provided by alternative.me
                (attribution required).
              </HelpTip>
            </span>
          }
          note={
            sentiment.fear_greed_value != null
              ? `Latest: ${sentiment.fear_greed_value}/100 (${sentiment.fear_greed_class || '—'}) · Source: alternative.me`
              : 'Source: alternative.me'
          }
        >
          {fngOption ? (
            <ReactECharts option={fngOption} style={{ height: 140 }} notMerge />
          ) : (
            <EmptyState text="Fear & Greed history unavailable" />
          )}
        </Panel>
      </div>

      {/* API vs Chain Verification Table (§5.2, §10.3) */}
      {!bqControl.show ? (
        <BigQueryLocked
          title="API vs On-Chain Verification Table (BigQuery)"
          description="Cross-checks public API metrics (mempool.space, Coin Metrics) against independent BigQuery crypto_bitcoin rollups (hashrate, tx count, active addresses, scheduled issuance, fees, miner revenue, and—once a UTXO baseline exists—circulating supply and MVRV)."
          bigquery={bigquery}
          onEnableDisplay={bqControl.available ? bqControl.toggle : undefined}
        />
      ) : (
        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              <CheckCircle2 size={15} className="text-emerald-400" />
              API vs On-Chain Verification
              <HelpTip label="Independent Cross-Verification (§10.3)">
                Compares public API figures against BigLens&apos;s own integer-satoshi BigQuery rollups (Jobs A1, A2, B,
                and C). Supply and MVRV require a one-off UTXO baseline snapshot and display &ldquo;needs
                baseline&rdquo; until one has been built.
              </HelpTip>
            </span>
          }
          note="Public API telemetry vs BigQuery crypto_bitcoin rollups"
        >
          <div className="mb-3 flex justify-end">
            <button
              type="button"
              onClick={() =>
                downloadCsv(
                  'bitcoin-verification.csv',
                  ['id', 'label', 'date', 'unit', 'api_label', 'api_value', 'chain_label', 'chain_value', 'delta_pct', 'status', 'note'],
                  verification.map((r) => [
                    r.id,
                    r.label,
                    r.date ?? '',
                    r.unit,
                    r.api_label,
                    r.api_value ?? '',
                    r.chain_label,
                    r.chain_value ?? '',
                    r.delta_pct != null ? r.delta_pct.toFixed(4) : '',
                    r.status,
                    r.note ?? '',
                  ]),
                )
              }
              className="inline-flex items-center gap-1 rounded-lg border border-zinc-700/80 bg-zinc-800/70 px-2.5 py-1 text-[11px] text-zinc-300 hover:bg-zinc-700/70"
            >
              <Download size={12} />
              CSV
            </button>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead>
                <tr className="border-b border-zinc-800 text-[11px] text-zinc-400">
                  <th className="py-2 pr-3 font-medium">Metric</th>
                  <th className="px-3 py-2 font-medium">Date</th>
                  <th className="px-3 py-2 font-medium">Public API</th>
                  <th className="px-3 py-2 text-right font-medium">API Value</th>
                  <th className="px-3 py-2 font-medium">BigQuery Chain</th>
                  <th className="px-3 py-2 text-right font-medium">Chain Value</th>
                  <th className="px-3 py-2 text-right font-medium">Δ%</th>
                  <th className="py-2 pl-3 text-right font-medium">Status</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/50">
                {verification.map((r: BtcVerificationRow) => (
                  <tr key={r.id} className="hover:bg-zinc-800/20">
                    <td className="py-2.5 pr-3 font-medium text-zinc-200">{r.label}</td>
                    <td className="px-3 py-2.5 font-mono text-zinc-400">{r.date || '—'}</td>
                    <td className="px-3 py-2.5 text-zinc-400">{r.api_label}</td>
                    <td className="px-3 py-2.5 text-right font-mono text-zinc-200">
                      {formatVerificationValue(r.api_value, r.unit)}
                    </td>
                    <td className="px-3 py-2.5 text-zinc-400">{r.chain_label}</td>
                    <td className="px-3 py-2.5 text-right font-mono text-zinc-200">
                      {r.status === 'needs_baseline' ? (
                        <span className="italic text-amber-300/90">needs baseline</span>
                      ) : (
                        formatVerificationValue(r.chain_value, r.unit)
                      )}
                    </td>
                    <td className="px-3 py-2.5 text-right font-mono text-zinc-300">
                      {r.delta_pct != null ? fmtPct(r.delta_pct, 2, true) : '—'}
                    </td>
                    <td className="py-2.5 pl-3 text-right">
                      <span
                        className={`inline-block rounded-full border px-2 py-0.5 text-[10px] font-medium ${
                          r.status === 'ok'
                            ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300'
                            : r.status === 'needs_baseline'
                              ? 'border-amber-500/30 bg-amber-500/10 text-amber-300'
                              : 'border-zinc-700 bg-zinc-800/60 text-zinc-400'
                        }`}
                      >
                        {r.note || r.status.replace(/_/g, ' ')}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Panel>
      )}
    </div>
  );
}

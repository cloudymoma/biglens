import { useCallback, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { AlertTriangle, Building2, CheckCircle2, Clock, Database, Download, Terminal } from 'lucide-react';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import { AXIS_LABEL, CHART_TOOLTIP, Panel, SPLIT_LINE } from '../crypto/shared';
import HelpTip from '../crypto/HelpTip';
import {
  confirmBitcoinBaseline,
  dryRunBitcoinBaseline,
  fetchBitcoinHolders,
  fetchBitcoinOnchainHolders,
} from './api';
import { downloadCsv, fmtPct, fmtUSD } from '../common/format';
import { fmtBTC, fmtBTCFromSats, fmtSats, satsToBTC } from './format';
import { usePolling } from '../common/usePolling';
import type { BitcoinBigQueryControl } from './useBitcoinBigQuery';
import type {
  BtcBaselinePlan,
  BtcBaselineStatus,
  BtcCoinageDay,
  BtcHolderDay,
  BtcMetaBigQuery,
} from './types';
import BigQueryLocked from './BigQueryLocked';
import SourceBadge from '../common/SourceBadge';

const AGE_BANDS: { key: string; label: string; color: string }[] = [
  { key: '<1d', label: '< 1d', color: '#f43f5e' },
  { key: '1d_1w', label: '1d–1w', color: '#fb7185' },
  { key: '1w_1m', label: '1w–1m', color: '#fb923c' },
  { key: '1m_3m', label: '1–3m', color: '#f59e0b' },
  { key: '3m_6m', label: '3–6m', color: '#eab308' },
  { key: '6m_12m', label: '6–12m', color: '#84cc16' },
  { key: '1y_2y', label: '1–2y', color: '#22c55e' },
  { key: '2y_3y', label: '2–3y', color: '#10b981' },
  { key: '3y_5y', label: '3–5y', color: '#14b8a6' },
  { key: '5y_7y', label: '5–7y', color: '#06b6d4' },
  { key: '7y_10y', label: '7–10y', color: '#3b82f6' },
  { key: '>10y', label: '> 10y', color: '#6366f1' },
];

export default function HoldersTab({
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
  const [baselinePlan, setBaselinePlan] = useState<BtcBaselinePlan | null>(null);
  const [lastPolledBaseline, setLastPolledBaseline] = useState<BtcBaselineStatus | null>(null);
  const [baselineOverride, setBaselineOverride] = useState<{
    forBaseline: BtcBaselineStatus | null;
    status: BtcBaselineStatus;
  } | null>(null);
  const [baselineBusy, setBaselineBusy] = useState<boolean>(false);
  const [baselineErr, setBaselineErr] = useState<string | null>(null);

  const fetchHolders = useCallback(() => fetchBitcoinHolders(days), [days]);
  const holdersPoll = usePolling(fetchHolders, 0, `btc-holders-${days}-${refreshTick}`);

  const fetchOnchain = useCallback(async () => {
    const res = await fetchBitcoinOnchainHolders(days);
    setLastPolledBaseline(res.baseline ?? null);
    return res;
  }, [days]);
  const activeBaselineForPoll =
    baselineOverride && baselineOverride.forBaseline === lastPolledBaseline
      ? baselineOverride.status
      : lastPolledBaseline;
  const onchainPollInterval =
    activeBaselineForPoll?.building || activeBaselineForPoll?.pending ? 30_000 : 0;
  const onchainPoll = usePolling(
    fetchOnchain,
    onchainPollInterval,
    `btc-onchain-holders-${days}-${refreshTick}`,
    bqControl.show,
  );

  const holdersData = holdersPoll.data;
  const flows = holdersData?.flows ?? [];
  const latestFlow = flows.length > 0 ? flows[flows.length - 1] : null;
  const net7dUSD = flows
    .slice(-7)
    .reduce((acc, r) => acc + (r.net_flow_usd ?? 0), 0);
  const treasuries = holdersData?.treasuries ?? null;

  const onchainData = onchainPoll.data;
  const coinageRows: BtcCoinageDay[] = onchainData?.coinage ?? [];
  const holderRows: BtcHolderDay[] = onchainData?.holders ?? [];
  const whales = onchainData?.whales ?? [];
  const polledBaseline = onchainData?.baseline ?? null;
  const baseline =
    baselineOverride && baselineOverride.forBaseline === polledBaseline
      ? baselineOverride.status
      : polledBaseline;
  const holderEngine = onchainData?.holder_engine ?? null;
  const coinageStatus = onchainData?.coinage_status ?? null;
  const holderWarnings = holderRows.flatMap(r =>
    (r.warnings ?? []).map(w => ({ date: r.date, warning: w })),
  );
  const latestCoinage = coinageRows.length > 0 ? coinageRows[coinageRows.length - 1] : null;
  const latestHolder = holderRows.length > 0 ? holderRows[holderRows.length - 1] : null;

  const extractBaselineError = (e: unknown, fallback: string): string => {
    if (typeof e === 'object' && e !== null && 'response' in e) {
      const resp = (e as { response?: { data?: unknown } }).response;
      const data = resp?.data;
      if (typeof data === 'string' && data.trim()) {
        return data.trim();
      }
      if (typeof data === 'object' && data !== null) {
        const msg =
          (data as { error?: unknown; message?: unknown }).error ??
          (data as { message?: unknown }).message;
        if (typeof msg === 'string' && msg.trim()) {
          return msg.trim();
        }
      }
    }
    return e instanceof Error ? e.message : fallback;
  };

  const handleDryRunBaseline = async () => {
    setBaselineBusy(true);
    setBaselineErr(null);
    try {
      const plan = await dryRunBitcoinBaseline();
      setBaselineOverride(null);
      setBaselinePlan(plan);
    } catch (e) {
      setBaselineErr(extractBaselineError(e, 'Baseline dry run failed'));
    } finally {
      setBaselineBusy(false);
    }
  };

  const handleConfirmBaseline = async () => {
    if (!baselinePlan?.token) return;
    setBaselineBusy(true);
    setBaselineErr(null);
    try {
      const st = await confirmBitcoinBaseline(baselinePlan.token);
      setBaselineOverride({ forBaseline: onchainData?.baseline ?? null, status: st });
      setBaselinePlan(null);
    } catch (e) {
      setBaselineErr(extractBaselineError(e, 'Baseline build failed'));
    } finally {
      setBaselineBusy(false);
    }
  };

  // Public API chart: Exchange Inflow / Outflow / Net Flow & Exchange BTC Balance
  const flowDates = flows.map(f => f.date.slice(5));
  const exchangeFlowOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 11 },
      data: ['Inflow ($M)', 'Outflow ($M)', 'Net Flow ($M)', 'Exchange Balance (BTC)'],
    },
    grid: { left: 56, right: 64, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: flowDates, axisLabel: AXIS_LABEL },
    yAxis: [
      {
        type: 'value',
        name: 'USD ($M)',
        nameTextStyle: AXIS_LABEL,
        axisLabel: AXIS_LABEL,
        splitLine: SPLIT_LINE,
      },
      {
        type: 'value',
        name: 'BTC',
        nameTextStyle: AXIS_LABEL,
        axisLabel: AXIS_LABEL,
        splitLine: { show: false },
      },
    ],
    series: [
      {
        name: 'Inflow ($M)',
        type: 'bar',
        stack: 'flow',
        itemStyle: { color: '#f87171' },
        data: flows.map(f => (f.inflow_usd != null ? Number((f.inflow_usd / 1e6).toFixed(2)) : null)),
      },
      {
        name: 'Outflow ($M)',
        type: 'bar',
        stack: 'flow',
        itemStyle: { color: '#34d399' },
        data: flows.map(f => (f.outflow_usd != null ? Number((-f.outflow_usd / 1e6).toFixed(2)) : null)),
      },
      {
        name: 'Net Flow ($M)',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#f59e0b' },
        lineStyle: { width: 1.75 },
        data: flows.map(f => (f.net_flow_usd != null ? Number((f.net_flow_usd / 1e6).toFixed(2)) : null)),
      },
      {
        name: 'Exchange Balance (BTC)',
        type: 'line',
        yAxisIndex: 1,
        showSymbol: false,
        itemStyle: { color: '#38bdf8' },
        lineStyle: { width: 2 },
        data: flows.map(f => (f.supply_ex_btc != null ? Number(f.supply_ex_btc.toFixed(0)) : null)),
      },
    ],
  };

  // BigQuery charts: Coin-age metrics
  const caDates = coinageRows.map(r => r.date.slice(5));
  const cddDormancyOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 11 },
      data: ['Coin Days Destroyed (BTC-days)', 'Dormancy (days)'],
    },
    grid: { left: 60, right: 56, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: caDates, axisLabel: AXIS_LABEL },
    yAxis: [
      {
        type: 'value',
        name: 'BTC-days',
        nameTextStyle: AXIS_LABEL,
        axisLabel: AXIS_LABEL,
        splitLine: SPLIT_LINE,
      },
      {
        type: 'value',
        name: 'Days',
        nameTextStyle: AXIS_LABEL,
        axisLabel: AXIS_LABEL,
        splitLine: { show: false },
      },
    ],
    series: [
      {
        name: 'Coin Days Destroyed (BTC-days)',
        type: 'bar',
        itemStyle: { color: '#f59e0b' },
        data: coinageRows.map(r => Number(r.cdd_btc_days.toFixed(1))),
      },
      {
        name: 'Dormancy (days)',
        type: 'line',
        yAxisIndex: 1,
        showSymbol: false,
        itemStyle: { color: '#38bdf8' },
        lineStyle: { width: 2 },
        data: coinageRows.map(r => Number(r.dormancy_days.toFixed(2))),
      },
    ],
  };

  const revivedSupplyOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 11 },
      data: ['Revived ≥ 1Y (BTC)', 'Revived ≥ 2Y (BTC)', 'Revived ≥ 5Y (BTC)'],
    },
    grid: { left: 56, right: 16, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: caDates, axisLabel: AXIS_LABEL },
    yAxis: {
      type: 'value',
      name: 'BTC',
      nameTextStyle: AXIS_LABEL,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    series: [
      {
        name: 'Revived ≥ 1Y (BTC)',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#34d399' },
        data: coinageRows.map(r => Number(satsToBTC(r.revived_1y_sats).toFixed(2))),
      },
      {
        name: 'Revived ≥ 2Y (BTC)',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#38bdf8' },
        data: coinageRows.map(r => Number(satsToBTC(r.revived_2y_sats).toFixed(2))),
      },
      {
        name: 'Revived ≥ 5Y (BTC)',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#a855f7' },
        data: coinageRows.map(r => Number(satsToBTC(r.revived_5y_sats).toFixed(2))),
      },
    ],
  };

  const spentAgeSharesOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 10 },
      data: AGE_BANDS.map(b => b.label),
    },
    grid: { left: 48, right: 16, top: 44, bottom: 28 },
    xAxis: { type: 'category', data: caDates, axisLabel: AXIS_LABEL },
    yAxis: {
      type: 'value',
      name: '%',
      max: 100,
      nameTextStyle: AXIS_LABEL,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    series: AGE_BANDS.map(b => ({
      name: b.label,
      type: 'line',
      stack: 'spent_age',
      areaStyle: { opacity: 0.45 },
      showSymbol: false,
      itemStyle: { color: b.color },
      data: coinageRows.map(r => Number((r.spent_age_shares_pct?.[b.key] ?? 0).toFixed(2))),
    })),
  };

  const soprOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 11 },
      data: ['SOPR', 'aSOPR (excl. <1d)', 'STH-SOPR', 'LTH-SOPR'],
    },
    grid: { left: 52, right: 16, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: caDates, axisLabel: AXIS_LABEL },
    yAxis: {
      type: 'value',
      scale: true,
      name: 'Ratio',
      nameTextStyle: AXIS_LABEL,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    series: [
      {
        name: 'SOPR',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#f59e0b' },
        data: coinageRows.map(r => (r.sopr != null ? Number(r.sopr.toFixed(4)) : null)),
      },
      {
        name: 'aSOPR (excl. <1d)',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#38bdf8' },
        data: coinageRows.map(r => (r.sopr_adjusted != null ? Number(r.sopr_adjusted.toFixed(4)) : null)),
      },
      {
        name: 'STH-SOPR',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#fb7185' },
        data: coinageRows.map(r => (r.sopr_sth != null ? Number(r.sopr_sth.toFixed(4)) : null)),
      },
      {
        name: 'LTH-SOPR',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#34d399' },
        data: coinageRows.map(r => (r.sopr_lth != null ? Number(r.sopr_lth.toFixed(4)) : null)),
      },
    ],
  };

  // Holder state charts (requires baseline)
  const hDates = holderRows.map(r => r.date.slice(5));
  const hodlWavesOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 10 },
      data: AGE_BANDS.map(b => b.label),
    },
    grid: { left: 48, right: 16, top: 44, bottom: 28 },
    xAxis: { type: 'category', data: hDates, axisLabel: AXIS_LABEL },
    yAxis: {
      type: 'value',
      name: '%',
      max: 100,
      nameTextStyle: AXIS_LABEL,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    series: AGE_BANDS.map(b => ({
      name: b.label,
      type: 'line',
      stack: 'hodl',
      areaStyle: { opacity: 0.5 },
      showSymbol: false,
      itemStyle: { color: b.color },
      data: holderRows.map(r => Number((r.hodl_waves_pct?.[b.key] ?? 0).toFixed(2))),
    })),
  };

  const lthSthSupplyOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 11 },
      data: ['LTH Supply (≥ 155d)', 'STH Supply (< 155d)'],
    },
    grid: { left: 60, right: 16, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: hDates, axisLabel: AXIS_LABEL },
    yAxis: {
      type: 'value',
      scale: true,
      name: 'BTC',
      nameTextStyle: AXIS_LABEL,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    series: [
      {
        name: 'LTH Supply (≥ 155d)',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#38bdf8' },
        lineStyle: { width: 2 },
        data: holderRows.map(r => Number(satsToBTC(r.lth_supply_sats).toFixed(0))),
      },
      {
        name: 'STH Supply (< 155d)',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#f59e0b' },
        lineStyle: { width: 2 },
        data: holderRows.map(r => Number(satsToBTC(r.sth_supply_sats).toFixed(0))),
      },
    ],
  };

  const realizedPricesOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 11 },
      data: ['Realized Price', 'STH Realized Price', 'LTH Realized Price'],
    },
    grid: { left: 60, right: 16, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: hDates, axisLabel: AXIS_LABEL },
    yAxis: {
      type: 'value',
      scale: true,
      name: 'USD',
      nameTextStyle: AXIS_LABEL,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    series: [
      {
        name: 'Realized Price',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#f59e0b' },
        lineStyle: { width: 2 },
        data: holderRows.map(r => (r.realized_price_usd != null ? Number(r.realized_price_usd.toFixed(2)) : null)),
      },
      {
        name: 'STH Realized Price',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#fb7185' },
        data: holderRows.map(r =>
          r.sth_realized_price_usd != null ? Number(r.sth_realized_price_usd.toFixed(2)) : null,
        ),
      },
      {
        name: 'LTH Realized Price',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#38bdf8' },
        data: holderRows.map(r =>
          r.lth_realized_price_usd != null ? Number(r.lth_realized_price_usd.toFixed(2)) : null,
        ),
      },
    ],
  };

  const mvrvProfitOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 11 },
      data: ['BigLens MVRV', 'Supply in Profit (%)'],
    },
    grid: { left: 48, right: 48, top: 32, bottom: 28 },
    xAxis: { type: 'category', data: hDates, axisLabel: AXIS_LABEL },
    yAxis: [
      {
        type: 'value',
        scale: true,
        name: 'MVRV',
        nameTextStyle: AXIS_LABEL,
        axisLabel: AXIS_LABEL,
        splitLine: SPLIT_LINE,
      },
      {
        type: 'value',
        min: 0,
        max: 100,
        name: 'Profit %',
        nameTextStyle: AXIS_LABEL,
        axisLabel: AXIS_LABEL,
        splitLine: { show: false },
      },
    ],
    series: [
      {
        name: 'BigLens MVRV',
        type: 'line',
        showSymbol: false,
        itemStyle: { color: '#f59e0b' },
        lineStyle: { width: 2 },
        data: holderRows.map(r => (r.mvrv != null ? Number(r.mvrv.toFixed(3)) : null)),
      },
      {
        name: 'Supply in Profit (%)',
        type: 'line',
        yAxisIndex: 1,
        showSymbol: false,
        itemStyle: { color: '#34d399' },
        data: holderRows.map(r =>
          r.supply_in_profit_pct != null ? Number(r.supply_in_profit_pct.toFixed(2)) : null,
        ),
      },
    ],
  };

  const exportTreasuriesCsv = () => {
    if (!treasuries?.companies?.length) return;
    downloadCsv(
      `bitcoin-public-treasuries.csv`,
      ['rank', 'name', 'symbol', 'country', 'total_holdings_btc', 'entry_value_usd', 'current_value_usd', 'pct_of_21m'],
      treasuries.companies.map((c, idx) => [
        idx + 1,
        c.name,
        c.symbol,
        c.country,
        c.total_holdings_btc,
        c.total_entry_value_usd,
        c.total_current_value_usd,
        c.pct_of_total_supply,
      ]),
    );
  };

  const exportWhalesCsv = () => {
    if (!whales.length) return;
    downloadCsv(
      `bitcoin-dormant-whale-revivals-${days}d.csv`,
      ['spend_day', 'create_day', 'age_days', 'btc', 'sats', 'inputs', 'spend_tx', 'spent_at'],
      whales.map(w => [
        w.spend_day,
        w.create_day,
        w.age_days,
        w.btc.toFixed(8),
        w.sats,
        w.inputs,
        w.spend_tx,
        w.spent_at,
      ]),
    );
  };

  return (
    <div className="space-y-6">
      {holdersPoll.error && <ErrorBanner message={holdersPoll.error} />}

      {/* Public API Section: Exchange Flows & Balance */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-4">
          <div className="flex items-center justify-between text-xs text-zinc-400">
            <span>Latest Exchange Net Flow</span>
            {latestFlow?.provisional && (
              <span className="rounded bg-amber-500/15 px-1.5 py-0.5 text-[10px] font-medium text-amber-300">
                Provisional
              </span>
            )}
          </div>
          <div
            className={`mt-1.5 font-mono text-lg font-semibold ${
              (latestFlow?.net_flow_usd ?? 0) < 0 ? 'text-emerald-400' : 'text-rose-400'
            }`}
          >
            {latestFlow?.net_flow_usd != null ? fmtUSD(latestFlow.net_flow_usd) : '—'}
          </div>
          <div className="mt-1 text-[11px] text-zinc-500">
            {latestFlow ? `As of ${latestFlow.date} (negative = net outflow)` : 'Coin Metrics FlowInExUSD − FlowOutExUSD'}
          </div>
        </div>

        <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-4">
          <div className="text-xs text-zinc-400">7D Exchange Net Flow</div>
          <div
            className={`mt-1.5 font-mono text-lg font-semibold ${
              net7dUSD < 0 ? 'text-emerald-400' : 'text-rose-400'
            }`}
          >
            {flows.length > 0 ? fmtUSD(net7dUSD) : '—'}
          </div>
          <div className="mt-1 text-[11px] text-zinc-500">Rolling 7-day net USD flow</div>
        </div>

        <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-4">
          <div className="text-xs text-zinc-400">Exchange BTC Balance</div>
          <div className="mt-1.5 font-mono text-lg font-semibold text-zinc-100">
            {latestFlow?.supply_ex_btc != null ? fmtBTC(latestFlow.supply_ex_btc, 0) : '—'}
          </div>
          <div className="mt-1 text-[11px] text-zinc-500">
            Share of supply: {fmtPct(latestFlow?.exchange_share_pct, 2)}
          </div>
        </div>

        <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-4">
          <div className="flex items-center gap-1.5 text-xs text-zinc-400">
            <Building2 className="h-3.5 w-3.5 text-amber-400" />
            <span>Public Corporate Treasuries</span>
          </div>
          <div className="mt-1.5 font-mono text-lg font-semibold text-zinc-100">
            {treasuries ? fmtBTC(treasuries.total_holdings_btc, 0) : '—'}
          </div>
          <div className="mt-1 text-[11px] text-zinc-500">
            {treasuries
              ? `${fmtUSD(treasuries.total_value_usd)} · ${fmtPct(treasuries.market_cap_dominance_pct, 2)} dominance`
              : 'Top 20 public companies (CoinGecko)'}
          </div>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              <span>Exchange Flows &amp; Tracked Balance</span>
              <HelpTip label="Coin Metrics exchange flows">
                Coin Metrics Community exchange flow estimates (FlowInExUSD, FlowOutExUSD) and tracked exchange supply
                (SplyExNtv). Flash rows for the latest UTC day are marked Provisional.
              </HelpTip>
            </span>
          }
          note="Coin Metrics Community · CC BY-NC 4.0"
        >
          {flows.length === 0 ? (
            <EmptyState text="Loading exchange flow history..." />
          ) : (
            <ReactECharts option={exchangeFlowOption} style={{ height: 290 }} notMerge />
          )}
        </Panel>

        <Panel
          title={
            <span className="inline-flex items-center gap-2">
              <span>Public Company Bitcoin Treasuries (Top 20)</span>
              <button
                type="button"
                onClick={exportTreasuriesCsv}
                disabled={!treasuries?.companies?.length}
                className="inline-flex items-center gap-1 rounded-md border border-zinc-700/80 bg-zinc-800/70 px-2 py-0.5 text-[11px] font-normal text-zinc-300 hover:text-zinc-100 disabled:opacity-40"
              >
                <Download className="h-3 w-3" />
                <span>CSV</span>
              </button>
            </span>
          }
          note="CoinGecko · Disclosed corporate holdings"
        >
          {!treasuries?.companies?.length ? (
            <EmptyState text="No public treasury data available" />
          ) : (
            <div className="max-h-[290px] overflow-y-auto">
              <table className="w-full text-left text-xs">
                <thead className="sticky top-0 bg-zinc-900/95 text-[11px] uppercase tracking-wider text-zinc-400">
                  <tr className="border-b border-zinc-800">
                    <th className="py-2 pr-2">#</th>
                    <th className="py-2 pr-2">Company</th>
                    <th className="py-2 pr-2">Ticker</th>
                    <th className="py-2 pr-2 text-right">Holdings (BTC)</th>
                    <th className="py-2 pr-2 text-right">Current Value</th>
                    <th className="py-2 text-right">% of 21M</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-zinc-800/60">
                  {treasuries.companies.map((c, idx) => (
                    <tr key={`${c.symbol}-${idx}`} className="hover:bg-zinc-800/30">
                      <td className="py-1.5 pr-2 font-mono text-zinc-500">{idx + 1}</td>
                      <td className="py-1.5 pr-2 font-medium text-zinc-200">
                        {c.name}{' '}
                        <span className="text-[10px] text-zinc-500">({c.country})</span>
                      </td>
                      <td className="py-1.5 pr-2 font-mono text-[11px] text-amber-300">{c.symbol}</td>
                      <td className="py-1.5 pr-2 text-right font-mono text-zinc-100">
                        {Math.round(c.total_holdings_btc).toLocaleString()}
                      </td>
                      <td className="py-1.5 pr-2 text-right font-mono text-zinc-300">
                        {fmtUSD(c.total_current_value_usd)}
                      </td>
                      <td className="py-1.5 text-right font-mono text-zinc-400">
                        {fmtPct(c.pct_of_total_supply, 3)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Panel>
      </div>

      {holdersData?.sources && holdersData.sources.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[11px] font-medium text-zinc-500">Public API sources:</span>
          {holdersData.sources.map(s => (
            <SourceBadge key={s.id} source={s} />
          ))}
        </div>
      )}

      {/* BigQuery On-Chain Section */}
      {!bqControl.show ? (
        <BigQueryLocked
          title="BigQuery Holder Behavior, Coin Age, SOPR & HODL Waves"
          description={bqControl.reason}
          bigquery={bigquery}
          onEnableDisplay={bqControl.available ? bqControl.toggle : undefined}
        />
      ) : (
        <div className="space-y-6">
          {onchainPoll.error && <ErrorBanner message={onchainPoll.error} />}

          {holderEngine?.state === 'halted' && (
            <div className="rounded-xl border border-rose-500/40 bg-rose-950/40 p-4 text-xs text-rose-100">
              <div className="flex flex-wrap items-center justify-between gap-2 font-semibold text-rose-200">
                <span className="inline-flex items-center gap-1.5">
                  <AlertTriangle className="h-4 w-4 text-rose-400" />
                  <span>
                    Holder Engine Halted on{' '}
                    <span className="font-mono">{holderEngine.halted_day || 'unknown day'}</span>
                  </span>
                </span>
                {holderEngine.since && (
                  <span className="font-mono text-[11px] text-rose-300/80">Since {holderEngine.since}</span>
                )}
              </div>
              <div className="mt-1.5 text-rose-200/90">
                Reason: <span className="font-mono">{holderEngine.reason || 'invariant_violation'}</span>
              </div>
              <div className="mt-1.5 flex flex-wrap items-center gap-2 text-rose-200/90">
                <span>Remedy:</span>
                <code className="rounded bg-zinc-950/80 px-2 py-0.5 font-mono text-[11px] text-amber-300">
                  {holderEngine.remedy ||
                    'Rebuild the UTXO baseline (~687 GiB scan, ~$4.20; run without --yes first to dry-run and confirm estimated cost, add --force if within min_interval_days): ./biglens-server --bitcoin-utxo-baseline --yes'}
                </code>
              </div>
            </div>
          )}

          {coinageStatus && (
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-zinc-800/80 bg-zinc-900/50 px-3.5 py-2 text-xs text-zinc-300">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-zinc-400">Coin-Age Sync (`btc_coinage`):</span>
                <span className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-[11px] text-zinc-200">
                  {coinageStatus.state}
                </span>
                <span className="text-zinc-400">
                  Synced through:{' '}
                  <span className="font-mono text-zinc-200">{coinageStatus.synced_through || '—'}</span>
                </span>
              </div>
              {coinageStatus.state === 'stopped' && (
                <div className="flex items-center gap-1.5 text-[11px] text-zinc-400">
                  <Terminal className="h-3.5 w-3.5 text-amber-400" />
                  <span>Resume CLI:</span>
                  <code className="rounded bg-zinc-950 px-2 py-0.5 font-mono text-amber-300">
                    ./biglens-server --bitcoin-bq-resume btc_coinage
                  </code>
                </div>
              )}
            </div>
          )}

          {holderWarnings.length > 0 && (
            <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3.5 py-2 text-xs text-amber-200">
              <div className="font-medium">On-Chain Holder Warnings:</div>
              <ul className="mt-1 list-inside list-disc space-y-0.5 font-mono text-[11px]">
                {holderWarnings.map((hw, i) => (
                  <li key={`${hw.date}-${i}`}>
                    {hw.date}: {hw.warning}
                  </li>
                ))}
              </ul>
            </div>
          )}

          {/* Baseline Status & Reconciliation Card */}
          <Panel
            title={
              <span className="inline-flex items-center gap-1.5">
                <span>UTXO Baseline &amp; Supply Reconciliation</span>
                <HelpTip label="UTXO baseline snapshot">
                  HODL waves, LTH/STH supply, Realized Cap, and BigLens MVRV require a UTXO baseline snapshot (Job C).
                  Once built via CLI, hourly maintenance applies the baseline and rolls state forward.
                </HelpTip>
              </span>
            }
            note="Exact integer-satoshi UTXO set snapshot · atomic handoff"
          >
            <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
              <div className="space-y-1.5 rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-3 text-xs">
                <div className="flex items-center justify-between">
                  <span className="text-zinc-400">Baseline State</span>
                  {baseline?.available ? (
                    <span
                      className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] font-medium ${
                        baseline.building || baseline.pending
                          ? 'bg-amber-500/15 text-amber-300'
                          : 'bg-emerald-500/15 text-emerald-300'
                      }`}
                    >
                      {baseline.building ? (
                        <>
                          <Clock className="h-3 w-3" /> Building…
                        </>
                      ) : baseline.pending ? (
                        <>
                          <Clock className="h-3 w-3" /> Pending Handoff
                        </>
                      ) : (
                        <>
                          <CheckCircle2 className="h-3 w-3" /> Active (Gen #{baseline.generation})
                        </>
                      )}
                    </span>
                  ) : baseline?.building || baseline?.pending ? (
                    <span className="inline-flex items-center gap-1 rounded bg-amber-500/15 px-1.5 py-0.5 text-[11px] font-medium text-amber-300">
                      <Clock className="h-3 w-3" /> {baseline.building ? 'Building…' : 'Pending Handoff'}
                    </span>
                  ) : (
                    <span className="rounded bg-zinc-800 px-1.5 py-0.5 text-[11px] text-zinc-400">
                      Not Built
                    </span>
                  )}
                </div>
                <div className="font-mono text-sm font-semibold text-zinc-100">
                  {baseline?.available ? `As of < ${baseline.as_of}` : 'No UTXO baseline stored'}
                </div>
                <div className="text-[11px] text-zinc-500">
                  {baseline?.built_at ? `Built at ${baseline.built_at}` : 'Run CLI command to initialize'}
                </div>
              </div>

              <div className="space-y-1.5 rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-3 text-xs">
                <div className="flex items-center justify-between">
                  <span className="text-zinc-400">Supply Reconciliation</span>
                  {baseline?.suspect && (
                    <span className="inline-flex items-center gap-1 rounded bg-rose-500/15 px-1.5 py-0.5 text-[11px] font-medium text-rose-300">
                      <AlertTriangle className="h-3 w-3" /> |Δ| &gt; 0.01%
                    </span>
                  )}
                </div>
                <div className="font-mono text-sm font-semibold text-zinc-100">
                  {baseline?.available ? fmtBTCFromSats(baseline.baseline_sats, 4) : '—'}
                </div>
                <div className="text-[11px] text-zinc-500">
                  {baseline?.available && baseline.expected_sats > 0 ? (
                    <>
                      Expected: {fmtBTCFromSats(baseline.expected_sats, 4)} (Δ{' '}
                      {baseline.delta_pct != null ? `${baseline.delta_pct.toFixed(5)}%` : '0%'})
                    </>
                  ) : (
                    'Reconciled against scheduled issuance − genesis − burns'
                  )}
                  {baseline?.cm_sply_cur_btc != null && (
                    <span className="block text-zinc-500">
                      Coin Metrics SplyCur: {fmtBTC(baseline.cm_sply_cur_btc, 2)}
                    </span>
                  )}
                </div>
              </div>

              <div className="space-y-2 rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-3 text-xs">
                <div className="flex items-center justify-between text-zinc-400">
                  <span className="inline-flex items-center gap-1">
                    <Terminal className="h-3.5 w-3.5 text-amber-400" /> Operator CLI
                  </span>
                  <span className="text-[11px] text-zinc-500">
                    Min interval: {baseline?.min_interval_days ?? 30}d
                  </span>
                </div>
                <code className="block rounded bg-zinc-950 px-2 py-1 font-mono text-[11px] text-amber-300">
                  {baseline?.cli_command || './biglens-server --bitcoin-utxo-baseline'}
                </code>
                {baseline?.next_allowed_at && (
                  <div className="text-[11px] text-zinc-500">
                    Next allowed run: <span className="font-mono text-zinc-400">{baseline.next_allowed_at}</span>
                  </div>
                )}
                {baseline?.allow_ui && (
                  <div className="flex flex-wrap items-center gap-2 pt-1">
                    {!baselinePlan ? (
                      <button
                        type="button"
                        disabled={baselineBusy}
                        onClick={handleDryRunBaseline}
                        className="rounded-md bg-amber-500/20 px-2.5 py-1 text-[11px] font-medium text-amber-300 ring-1 ring-amber-500/40 hover:bg-amber-500/30 disabled:opacity-50"
                      >
                        {baselineBusy ? 'Running dry run...' : 'Dry Run UTXO Baseline'}
                      </button>
                    ) : (
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-[11px] text-zinc-300">
                          Est: {baselinePlan.estimated_gib.toFixed(1)} GiB ({fmtUSD(baselinePlan.estimated_usd, 2)})
                        </span>
                        <button
                          type="button"
                          disabled={baselineBusy}
                          onClick={handleConfirmBaseline}
                          className="rounded-md bg-emerald-500 px-2.5 py-1 text-[11px] font-semibold text-zinc-950 hover:bg-emerald-400 disabled:opacity-50"
                        >
                          {baselineBusy ? 'Building...' : 'Confirm & Build'}
                        </button>
                      </div>
                    )}
                    {baselineErr && <span className="text-[11px] text-rose-400">{baselineErr}</span>}
                  </div>
                )}
              </div>
            </div>
          </Panel>

          {/* Coin-Age KPIs & Charts */}
          {coinageRows.length === 0 ? (
            <Panel
              title="BigQuery Coin-Age & Spent-Output Analytics"
              note="Weekly coin-age job (btc_coinage) · bigquery-public-data.crypto_bitcoin"
            >
              <EmptyState text="No coin-age rows stored yet. Enable bitcoin.bigquery.jobs in conf.yaml to populate." />
            </Panel>
          ) : (
            <>
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
                <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-4">
                  <div className="text-xs text-zinc-400">Latest Coin Days Destroyed</div>
                  <div className="mt-1.5 font-mono text-lg font-semibold text-amber-300">
                    {latestCoinage
                      ? `${latestCoinage.cdd_btc_days.toLocaleString(undefined, { maximumFractionDigits: 1 })} BTC-d`
                      : '—'}
                  </div>
                  <div className="mt-1 text-[11px] text-zinc-500">
                    {latestCoinage ? `Spent volume: ${fmtBTCFromSats(latestCoinage.spent_sats, 1)}` : '—'}
                  </div>
                </div>

                <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-4">
                  <div className="text-xs text-zinc-400">Average Dormancy</div>
                  <div className="mt-1.5 font-mono text-lg font-semibold text-zinc-100">
                    {latestCoinage ? `${latestCoinage.dormancy_days.toFixed(2)} d` : '—'}
                  </div>
                  <div className="mt-1 text-[11px] text-zinc-500">CDD ÷ daily spent BTC</div>
                </div>

                <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-4">
                  <div className="text-xs text-zinc-400">Revived Supply (≥ 1Y / ≥ 5Y)</div>
                  <div className="mt-1.5 font-mono text-lg font-semibold text-emerald-400">
                    {latestCoinage
                      ? `${fmtBTCFromSats(latestCoinage.revived_1y_sats, 1)} / ${fmtBTCFromSats(
                          latestCoinage.revived_5y_sats,
                          1,
                        )}`
                      : '—'}
                  </div>
                  <div className="mt-1 text-[11px] text-zinc-500">Coins dormant ≥ 365d / ≥ 1,825d spent</div>
                </div>

                <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-4">
                  <div className="text-xs text-zinc-400">Latest SOPR (All / aSOPR)</div>
                  <div className="mt-1.5 font-mono text-lg font-semibold text-zinc-100">
                    {latestCoinage?.sopr != null
                      ? `${latestCoinage.sopr.toFixed(4)} / ${(latestCoinage.sopr_adjusted ?? 0).toFixed(4)}`
                      : 'Blocked'}
                  </div>
                  <div className="mt-1 text-[11px] text-zinc-500">
                    {latestCoinage?.blocked
                      ? `Gate: ${latestCoinage.blocked}`
                      : `LTH: ${latestCoinage?.sopr_lth?.toFixed(3) ?? '—'} · STH: ${
                          latestCoinage?.sopr_sth?.toFixed(3) ?? '—'
                        }`}
                  </div>
                </div>
              </div>

              <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
                <Panel
                  title="Coin Days Destroyed (CDD) & Dormancy"
                  note="Exact satoshi-day destruction (negative midnight-inversion ages clamped to 0)"
                >
                  <ReactECharts option={cddDormancyOption} style={{ height: 280 }} notMerge />
                </Panel>

                <Panel
                  title="Revived Old Supply (≥ 1Y, ≥ 2Y, ≥ 5Y)"
                  note="Daily BTC volume spent from outputs aged at least 1, 2, or 5 years"
                >
                  <ReactECharts option={revivedSupplyOption} style={{ height: 280 }} notMerge />
                </Panel>
              </div>

              <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
                <Panel
                  title="Spent Output Age Distribution"
                  note="Share of daily spent satoshis across 12 creation-age bands"
                >
                  <ReactECharts option={spentAgeSharesOption} style={{ height: 300 }} notMerge />
                </Panel>

                <Panel
                  title={
                    <span className="inline-flex items-center gap-1.5">
                      <span>Spent Output Profit Ratio (SOPR)</span>
                      <HelpTip label="SOPR price gate">
                        Price-gated: SOPR is computed only when local daily USD price history covers 2010-08-18 through
                        the spend day without gaps. Outputs created before 2010-08-18 (p_c = 0) are excluded from the
                        SOPR numerator and denominator.
                      </HelpTip>
                    </span>
                  }
                  note="SOPR, aSOPR (excluding <1d), STH-SOPR (<155d) & LTH-SOPR (≥155d)"
                >
                  {latestCoinage?.blocked && (
                    <div className="mb-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-1.5 text-xs text-amber-200">
                      SOPR price gate active: <span className="font-mono">{latestCoinage.blocked}</span>
                    </div>
                  )}
                  <ReactECharts option={soprOption} style={{ height: 280 }} notMerge />
                </Panel>
              </div>

              {/* Dormant Whale Revivals Table */}
              <Panel
                title={
                  <span className="inline-flex items-center gap-2">
                    <span>Dormant Whale Revivals (≥ 10 BTC, Age ≥ 1Y)</span>
                    <button
                      type="button"
                      onClick={exportWhalesCsv}
                      disabled={whales.length === 0}
                      className="inline-flex items-center gap-1 rounded-md border border-zinc-700/80 bg-zinc-800/70 px-2 py-0.5 text-[11px] font-normal text-zinc-300 hover:text-zinc-100 disabled:opacity-40"
                    >
                      <Download className="h-3 w-3" />
                      <span>CSV</span>
                    </button>
                  </span>
                }
                note="Top 20 oldest/largest dormant UTXO spends per UTC day"
              >
                {whales.length === 0 ? (
                  <EmptyState text="No dormant whale revivals (≥ 10 BTC, ≥ 1Y old) in the selected window." />
                ) : (
                  <div className="max-h-[300px] overflow-y-auto">
                    <table className="w-full text-left text-xs">
                      <thead className="sticky top-0 bg-zinc-900/95 text-[11px] uppercase tracking-wider text-zinc-400">
                        <tr className="border-b border-zinc-800">
                          <th className="py-2 pr-3">Spend Day</th>
                          <th className="py-2 pr-3">Create Day</th>
                          <th className="py-2 pr-3 text-right">Age</th>
                          <th className="py-2 pr-3 text-right">Revived BTC</th>
                          <th className="py-2 pr-3 text-right">Satoshis</th>
                          <th className="py-2 pr-3 text-right">Inputs</th>
                          <th className="py-2">Transaction</th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-zinc-800/60 font-mono">
                        {whales.map((w, idx) => (
                          <tr key={`${w.spend_tx}-${w.create_day}-${idx}`} className="hover:bg-zinc-800/30">
                            <td className="py-1.5 pr-3 text-zinc-200">{w.spend_day}</td>
                            <td className="py-1.5 pr-3 text-amber-300">{w.create_day}</td>
                            <td className="py-1.5 pr-3 text-right text-zinc-300">
                              {w.age_days.toLocaleString()}d ({(w.age_days / 365.25).toFixed(1)}y)
                            </td>
                            <td className="py-1.5 pr-3 text-right font-semibold text-emerald-400">
                              {fmtBTC(w.btc, 2)}
                            </td>
                            <td className="py-1.5 pr-3 text-right text-zinc-400">{fmtSats(w.sats)}</td>
                            <td className="py-1.5 pr-3 text-right text-zinc-400">{w.inputs}</td>
                            <td className="py-1.5 text-zinc-300">
                              <a
                                href={`https://mempool.space/tx/${w.spend_tx}`}
                                target="_blank"
                                rel="noopener noreferrer"
                                className="text-sky-400 hover:underline"
                              >
                                {w.spend_tx.slice(0, 10)}…{w.spend_tx.slice(-8)}
                              </a>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </Panel>
            </>
          )}

          {/* Baseline-Dependent Holder State Panels: HODL Waves, LTH/STH, Realized Cap & MVRV */}
          {holderRows.length === 0 ? (
            <Panel
              title="HODL Waves, LTH/STH Supply, Realized Cap & BigLens MVRV"
              note="Requires a one-time UTXO baseline snapshot (~687 GiB ≈ $4.20)"
            >
              <div className="flex flex-col items-start gap-2 rounded-lg border border-zinc-800 bg-zinc-900/60 p-4 text-xs text-zinc-300">
                <div className="flex items-center gap-2 font-medium text-zinc-100">
                  <Database className="h-4 w-4 text-amber-400" />
                  <span>UTXO baseline required for full-supply cohort accounting</span>
                </div>
                <p className="text-zinc-400">
                  Coin-age panels above work without a baseline. To unlock HODL waves, LTH/STH supply split, Realized
                  Cap, Realized Price, Supply in Profit, and BigLens MVRV, build the baseline from the operator shell:
                </p>
                <code className="rounded bg-zinc-950 px-2.5 py-1 font-mono text-amber-300">
                  ./biglens-server --bitcoin-utxo-baseline --yes
                </code>
              </div>
            </Panel>
          ) : (
            <>
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
                <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-4">
                  <div className="text-xs text-zinc-400">Realized Cap / Price</div>
                  <div className="mt-1.5 font-mono text-lg font-semibold text-amber-300">
                    {latestHolder?.realized_cap_usd != null
                      ? fmtUSD(latestHolder.realized_cap_usd)
                      : 'Blocked'}
                  </div>
                  <div className="mt-1 text-[11px] text-zinc-500">
                    Realized price: {fmtUSD(latestHolder?.realized_price_usd, 0)}
                  </div>
                </div>

                <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-4">
                  <div className="text-xs text-zinc-400">LTH / STH Realized Price</div>
                  <div className="mt-1.5 font-mono text-lg font-semibold text-zinc-100">
                    {latestHolder?.lth_realized_price_usd != null
                      ? `${fmtUSD(latestHolder.lth_realized_price_usd, 0)} / ${fmtUSD(
                          latestHolder.sth_realized_price_usd,
                          0,
                        )}`
                      : 'Blocked'}
                  </div>
                  <div className="mt-1 text-[11px] text-zinc-500">155-day cohort threshold</div>
                </div>

                <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-4">
                  <div className="text-xs text-zinc-400">BigLens On-Chain MVRV</div>
                  <div className="mt-1.5 font-mono text-lg font-semibold text-sky-400">
                    {latestHolder?.mvrv != null ? latestHolder.mvrv.toFixed(3) : 'Blocked'}
                  </div>
                  <div className="mt-1 text-[11px] text-zinc-500">
                    Supply in profit: {fmtPct(latestHolder?.supply_in_profit_pct, 1)}
                  </div>
                </div>

                <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-4">
                  <div className="text-xs text-zinc-400">LTH vs STH Supply</div>
                  <div className="mt-1.5 font-mono text-lg font-semibold text-zinc-100">
                    {latestHolder
                      ? `${fmtBTCFromSats(latestHolder.lth_supply_sats, 0)} / ${fmtBTCFromSats(
                          latestHolder.sth_supply_sats,
                          0,
                        )}`
                      : '—'}
                  </div>
                  <div className="mt-1 text-[11px] text-zinc-500">
                    Total UTXO supply: {latestHolder ? fmtBTCFromSats(latestHolder.supply_sats, 2) : '—'}
                  </div>
                </div>
              </div>

              {latestHolder?.blocked && (
                <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3.5 py-2 text-xs text-amber-200">
                  Holder valuation gate active on {latestHolder.date}:{' '}
                  <span className="font-mono">{latestHolder.blocked}</span>
                </div>
              )}

              <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
                <Panel
                  title="HODL Waves (Unspent Supply by Age Band)"
                  note="Share of circulating UTXO supply across 12 creation-age cohorts"
                >
                  <ReactECharts option={hodlWavesOption} style={{ height: 300 }} notMerge />
                </Panel>

                <Panel
                  title="Long-Term (≥ 155d) vs Short-Term (< 155d) Holder Supply"
                  note="Exact satoshi supply split rolled forward from the UTXO baseline"
                >
                  <ReactECharts option={lthSthSupplyOption} style={{ height: 300 }} notMerge />
                </Panel>
              </div>

              <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
                <Panel
                  title="Realized Price Cohorts (All / STH / LTH)"
                  note="On-chain cost basis per BTC across all coins, STH (< 155d), and LTH (≥ 155d)"
                >
                  <ReactECharts option={realizedPricesOption} style={{ height: 280 }} notMerge />
                </Panel>

                <Panel
                  title="BigLens MVRV & Supply in Profit"
                  note="Market Value to Realized Value ratio and % of UTXO supply created below daily close"
                >
                  <ReactECharts option={mvrvProfitOption} style={{ height: 280 }} notMerge />
                </Panel>
              </div>
            </>
          )}

          {onchainData?.sources && onchainData.sources.length > 0 && (
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-[11px] font-medium text-zinc-500">On-chain sources:</span>
              {onchainData.sources.map(s => (
                <SourceBadge key={s.id} source={s} />
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

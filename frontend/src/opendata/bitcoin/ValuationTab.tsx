import { useCallback, useMemo, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { Download } from 'lucide-react';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import { AXIS_LABEL, CHART_TOOLTIP, Panel, SPLIT_LINE } from '../crypto/shared';
import HelpTip from '../crypto/HelpTip';
import { fetchBitcoinValuation } from './api';
import { downloadCsv, fmtPct, fmtUSD } from '../common/format';
import { usePolling } from '../common/usePolling';
import SourceBadge from '../common/SourceBadge';
import BigQueryLocked from './BigQueryLocked';
import type { BitcoinBigQueryControl } from './useBitcoinBigQuery';
import type { BtcMetaBigQuery, BtcValuationRange } from './types';

const RANGES: { id: BtcValuationRange; label: string }[] = [
  { id: '1y', label: '1Y' },
  { id: '4y', label: '4Y (1 Cycle)' },
  { id: 'all', label: 'All History' },
];

const CYCLE_COLORS = ['#a78bfa', '#38bdf8', '#34d399', '#f59e0b'];

const CHART_GRID = { left: 52, right: 20, top: 28, bottom: 28 };
const CHART_AXIS = {
  axisLine: { lineStyle: { color: '#27272a' } },
  axisTick: { show: false },
  axisLabel: AXIS_LABEL,
};

function ValuationCard({
  label,
  value,
  sub,
}: {
  label: string;
  value: string;
  sub: string;
}) {
  return (
    <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3.5">
      <div className="text-[11px] font-medium text-zinc-400">{label}</div>
      <div className="mt-1 font-mono text-lg font-bold text-zinc-100">{value}</div>
      <div className="mt-1 text-[11px] text-zinc-400">{sub}</div>
    </div>
  );
}

export default function ValuationTab({
  bqControl,
  bigquery,
  refreshTick,
}: {
  bqControl: BitcoinBigQueryControl;
  bigquery: BtcMetaBigQuery | null;
  refreshTick: number;
}) {
  const [range, setRange] = useState<BtcValuationRange>('4y');

  const fetcher = useCallback(() => fetchBitcoinValuation(range), [range]);
  const poll = usePolling(fetcher, 300_000, `btc-valuation-${range}-${refreshTick}`);

  const data = poll.data;
  const latest = data?.latest;
  const series = useMemo(() => data?.series ?? [], [data?.series]);
  const cycles = useMemo(() => data?.cycles ?? [], [data?.cycles]);
  const fng = useMemo(() => data?.fear_greed ?? [], [data?.fear_greed]);
  const showBQ = bqControl.show && bqControl.available;

  // 1. Price & Trend (Log y-axis) + 200DMA + 200WMA Heatmap (visualMap on 28d % change) + Realized Prices
  const priceTrendOpt = useMemo(() => {
    if (series.length === 0) return null;
    const dates = series.map(p => p.date);
    const heatmapPts = series
      .map((p, idx) =>
        p.wma200_monthly_pct != null && p.close_usd > 0
          ? [dates[idx], Number(p.close_usd.toFixed(2)), Number(p.wma200_monthly_pct.toFixed(2))]
          : null,
      )
      .filter((x): x is [string, number, number] => x !== null);

    const chartSeries: Record<string, unknown>[] = [
      {
        name: 'BTC Close (USD)',
        type: 'line',
        showSymbol: false,
        lineStyle: { width: 1.8, color: '#e4e4e7' },
        itemStyle: { color: '#e4e4e7' },
        data: series.map(p => (p.close_usd > 0 ? Number(p.close_usd.toFixed(2)) : null)),
      },
      {
        name: '200D SMA',
        type: 'line',
        showSymbol: false,
        lineStyle: { width: 1.5, type: 'dashed', color: '#38bdf8' },
        itemStyle: { color: '#38bdf8' },
        data: series.map(p => (p.sma200 && p.sma200 > 0 ? Number(p.sma200.toFixed(2)) : null)),
      },
      {
        name: '200W MA (1400D)',
        type: 'line',
        showSymbol: false,
        lineStyle: { width: 2, color: '#a78bfa' },
        itemStyle: { color: '#a78bfa' },
        data: series.map(p => (p.wma200 && p.wma200 > 0 ? Number(p.wma200.toFixed(2)) : null)),
      },
    ];

    if (heatmapPts.length > 0) {
      chartSeries.push({
        name: '200WMA 4W % Heatmap',
        type: 'scatter',
        symbolSize: range === '1y' ? 5 : 3.5,
        data: heatmapPts,
      });
    }

    if (showBQ) {
      if (series.some(p => p.realized_price_usd && p.realized_price_usd > 0)) {
        chartSeries.push({
          name: 'Realized Price (BigLens)',
          type: 'line',
          showSymbol: false,
          lineStyle: { width: 2, color: '#10b981' },
          itemStyle: { color: '#10b981' },
          data: series.map(p =>
            p.realized_price_usd && p.realized_price_usd > 0
              ? Number(p.realized_price_usd.toFixed(2))
              : null,
          ),
        });
      }
      if (series.some(p => p.sth_realized_price_usd && p.sth_realized_price_usd > 0)) {
        chartSeries.push({
          name: 'STH Realized (<155d)',
          type: 'line',
          showSymbol: false,
          lineStyle: { width: 1.4, type: 'dotted', color: '#f97316' },
          itemStyle: { color: '#f97316' },
          data: series.map(p =>
            p.sth_realized_price_usd && p.sth_realized_price_usd > 0
              ? Number(p.sth_realized_price_usd.toFixed(2))
              : null,
          ),
        });
      }
      if (series.some(p => p.lth_realized_price_usd && p.lth_realized_price_usd > 0)) {
        chartSeries.push({
          name: 'LTH Realized (≥155d)',
          type: 'line',
          showSymbol: false,
          lineStyle: { width: 1.4, type: 'dotted', color: '#22d3ee' },
          itemStyle: { color: '#22d3ee' },
          data: series.map(p =>
            p.lth_realized_price_usd && p.lth_realized_price_usd > 0
              ? Number(p.lth_realized_price_usd.toFixed(2))
              : null,
          ),
        });
      }
    }

    return {
      tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
      legend: { top: 0, textStyle: { ...AXIS_LABEL, fontSize: 11 } },
      visualMap:
        heatmapPts.length > 0
          ? {
              show: true,
              dimension: 2,
              seriesIndex: 3,
              min: 0,
              max: 14,
              orient: 'horizontal',
              right: 12,
              bottom: 0,
              itemWidth: 10,
              itemHeight: 70,
              text: ['14%+ / 4w', '0% / 4w'],
              textStyle: { ...AXIS_LABEL, fontSize: 10 },
              inRange: {
                color: ['#3b82f6', '#22d3ee', '#34d399', '#facc15', '#f97316', '#ef4444'],
              },
            }
          : undefined,
      grid: { ...CHART_GRID, top: 34, bottom: heatmapPts.length > 0 ? 44 : 28 },
      xAxis: { type: 'category', data: dates, ...CHART_AXIS },
      yAxis: {
        type: 'log',
        logBase: 10,
        ...CHART_AXIS,
        splitLine: SPLIT_LINE,
        axisLabel: {
          ...AXIS_LABEL,
          formatter: (v: number) => `$${v >= 1000 ? `${(v / 1000).toFixed(0)}k` : v}`,
        },
      },
      series: chartSeries,
    };
  }, [series, range, showBQ]);

  // 2. Drawdown from Running ATH
  const drawdownOpt = useMemo(() => {
    if (series.length === 0) return null;
    return {
      tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
      grid: { ...CHART_GRID, top: 20 },
      xAxis: { type: 'category', data: series.map(p => p.date), ...CHART_AXIS },
      yAxis: {
        type: 'value',
        max: 0,
        ...CHART_AXIS,
        splitLine: SPLIT_LINE,
        axisLabel: { ...AXIS_LABEL, formatter: (v: number) => `${v}%` },
      },
      series: [
        {
          name: 'Drawdown from ATH (%)',
          type: 'line',
          showSymbol: false,
          lineStyle: { width: 1.6, color: '#f43f5e' },
          itemStyle: { color: '#f43f5e' },
          areaStyle: { color: 'rgba(244, 63, 94, 0.16)' },
          data: series.map(p => Number(p.drawdown_pct.toFixed(2))),
        },
      ],
    };
  }, [series]);

  // 3. Mayer Multiple (Close / SMA200)
  const mayerOpt = useMemo(() => {
    const pts = series.filter(p => p.mayer != null);
    if (pts.length === 0) return null;
    return {
      tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
      grid: { ...CHART_GRID, top: 24 },
      xAxis: { type: 'category', data: pts.map(p => p.date), ...CHART_AXIS },
      yAxis: {
        type: 'value',
        ...CHART_AXIS,
        splitLine: SPLIT_LINE,
        axisLabel: { ...AXIS_LABEL, formatter: (v: number) => `${v.toFixed(1)}×` },
      },
      series: [
        {
          name: 'Mayer Multiple',
          type: 'line',
          showSymbol: false,
          lineStyle: { width: 2, color: '#f59e0b' },
          itemStyle: { color: '#f59e0b' },
          data: pts.map(p => Number((p.mayer ?? 0).toFixed(3))),
          markLine: {
            symbol: 'none',
            label: { ...AXIS_LABEL, fontSize: 10 },
            data: [
              { yAxis: 0.8, name: '0.8× Accumulation', lineStyle: { color: '#10b981', type: 'dashed' } },
              { yAxis: 1.0, name: '1.0× (200DMA)', lineStyle: { color: '#71717a', type: 'dotted' } },
              { yAxis: 2.4, name: '2.4× Overheated', lineStyle: { color: '#ef4444', type: 'dashed' } },
            ],
          },
        },
      ],
    };
  }, [series]);

  // 4. MVRV (Coin Metrics CapMVRVCur vs BigLens UTXO MVRV)
  const mvrvOpt = useMemo(() => {
    const pts = series.filter(p => p.cm_mvrv != null || (showBQ && p.biglens_mvrv != null));
    if (pts.length === 0) return null;
    const chartSeries: Record<string, unknown>[] = [
      {
        name: 'Coin Metrics MVRV (CapMVRVCur)',
        type: 'line',
        showSymbol: false,
        lineStyle: { width: 2, color: '#38bdf8' },
        itemStyle: { color: '#38bdf8' },
        data: pts.map(p => (p.cm_mvrv != null ? Number(p.cm_mvrv.toFixed(3)) : null)),
        markLine: {
          symbol: 'none',
          label: { ...AXIS_LABEL, fontSize: 10 },
          data: [
            { yAxis: 1.0, name: '1.0× Realized Value', lineStyle: { color: '#10b981', type: 'dashed' } },
            { yAxis: 3.0, name: '3.0× Cycle High Zone', lineStyle: { color: '#ef4444', type: 'dashed' } },
          ],
        },
      },
    ];
    if (showBQ && pts.some(p => p.biglens_mvrv != null)) {
      chartSeries.push({
        name: 'BigLens UTXO MVRV',
        type: 'line',
        showSymbol: false,
        lineStyle: { width: 2, type: 'dashed', color: '#f59e0b' },
        itemStyle: { color: '#f59e0b' },
        data: pts.map(p => (p.biglens_mvrv != null ? Number(p.biglens_mvrv.toFixed(3)) : null)),
      });
    }
    return {
      tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
      legend: { top: 0, textStyle: { ...AXIS_LABEL, fontSize: 11 } },
      grid: { ...CHART_GRID, top: 32 },
      xAxis: { type: 'category', data: pts.map(p => p.date), ...CHART_AXIS },
      yAxis: {
        type: 'value',
        ...CHART_AXIS,
        splitLine: SPLIT_LINE,
        axisLabel: { ...AXIS_LABEL, formatter: (v: number) => `${v.toFixed(1)}×` },
      },
      series: chartSeries,
    };
  }, [series, showBQ]);

  // 5. Puell Multiple (CM IssTotNtv vs Exact Chain Subsidy)
  const puellOpt = useMemo(() => {
    const pts = series.filter(p => p.puell_cm != null || (showBQ && p.puell_chain != null));
    if (pts.length === 0) return null;
    const chartSeries: Record<string, unknown>[] = [
      {
        name: 'Puell Multiple (Coin Metrics)',
        type: 'line',
        showSymbol: false,
        lineStyle: { width: 2, color: '#a78bfa' },
        itemStyle: { color: '#a78bfa' },
        data: pts.map(p => (p.puell_cm != null ? Number(p.puell_cm.toFixed(3)) : null)),
        markLine: {
          symbol: 'none',
          label: { ...AXIS_LABEL, fontSize: 10 },
          data: [
            { yAxis: 0.5, name: '0.5× Miner Stress', lineStyle: { color: '#10b981', type: 'dashed' } },
            { yAxis: 4.0, name: '4.0× Issuance Peak', lineStyle: { color: '#ef4444', type: 'dashed' } },
          ],
        },
      },
    ];
    if (showBQ && pts.some(p => p.puell_chain != null)) {
      chartSeries.push({
        name: 'Puell Multiple (Exact Subsidy, Job A2)',
        type: 'line',
        showSymbol: false,
        lineStyle: { width: 1.8, type: 'dashed', color: '#34d399' },
        itemStyle: { color: '#34d399' },
        data: pts.map(p => (p.puell_chain != null ? Number(p.puell_chain.toFixed(3)) : null)),
      });
    }
    return {
      tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
      legend: { top: 0, textStyle: { ...AXIS_LABEL, fontSize: 11 } },
      grid: { ...CHART_GRID, top: 32 },
      xAxis: { type: 'category', data: pts.map(p => p.date), ...CHART_AXIS },
      yAxis: {
        type: 'value',
        ...CHART_AXIS,
        splitLine: SPLIT_LINE,
        axisLabel: { ...AXIS_LABEL, formatter: (v: number) => `${v.toFixed(1)}×` },
      },
      series: chartSeries,
    };
  }, [series, showBQ]);

  // 6. Halving Cycles Overlay (Log y-axis, Indexed to 1.0x at Halving Day)
  const halvingCyclesOpt = useMemo(() => {
    if (cycles.length === 0) return null;
    return {
      tooltip: {
        ...CHART_TOOLTIP,
        trigger: 'axis',
      },
      legend: { top: 0, textStyle: { ...AXIS_LABEL, fontSize: 11 } },
      grid: { ...CHART_GRID, top: 32 },
      xAxis: {
        type: 'value',
        name: 'Days since halving',
        nameLocation: 'middle',
        nameGap: 22,
        min: 0,
        max: 1460,
        ...CHART_AXIS,
      },
      yAxis: {
        type: 'log',
        logBase: 10,
        ...CHART_AXIS,
        splitLine: SPLIT_LINE,
        axisLabel: { ...AXIS_LABEL, formatter: (v: number) => `${v}×` },
      },
      series: cycles.map((c, idx) => ({
        name: `${c.label} ($${c.base_price.toFixed(0)})`,
        type: 'line',
        showSymbol: false,
        lineStyle: {
          width: idx === cycles.length - 1 ? 2.5 : 1.5,
          color: CYCLE_COLORS[idx % CYCLE_COLORS.length],
        },
        itemStyle: { color: CYCLE_COLORS[idx % CYCLE_COLORS.length] },
        data: c.points.map(pt => [pt.day, Number(pt.indexed.toFixed(3))]),
      })),
    };
  }, [cycles]);

  // 7. Fear & Greed (365d)
  const fngOpt = useMemo(() => {
    if (fng.length === 0) return null;
    return {
      tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
      grid: { ...CHART_GRID, top: 24 },
      xAxis: { type: 'category', data: fng.map(p => p.date), ...CHART_AXIS },
      yAxis: {
        type: 'value',
        min: 0,
        max: 100,
        ...CHART_AXIS,
        splitLine: SPLIT_LINE,
      },
      series: [
        {
          name: 'Fear & Greed Index',
          type: 'line',
          showSymbol: false,
          lineStyle: { width: 1.8, color: '#f59e0b' },
          itemStyle: { color: '#f59e0b' },
          areaStyle: { color: 'rgba(245, 158, 11, 0.14)' },
          data: fng.map(p => p.value),
          markLine: {
            symbol: 'none',
            label: { ...AXIS_LABEL, fontSize: 10 },
            data: [
              { yAxis: 25, name: 'Extreme Fear (25)', lineStyle: { color: '#10b981', type: 'dashed' } },
              { yAxis: 50, name: 'Neutral (50)', lineStyle: { color: '#71717a', type: 'dotted' } },
              { yAxis: 75, name: 'Extreme Greed (75)', lineStyle: { color: '#ef4444', type: 'dashed' } },
            ],
          },
        },
      ],
    };
  }, [fng]);

  const exportSeriesCsv = () => {
    if (series.length === 0) return;
    downloadCsv(
      `btc-valuation-${range}.csv`,
      [
        'date',
        'close_usd',
        'sma200',
        'wma200',
        'wma200_monthly_pct',
        'mayer',
        'drawdown_pct',
        'cm_mvrv',
        'biglens_mvrv',
        'mvrv_delta_pct',
        'puell_cm',
        'puell_chain',
        'realized_price_usd',
        'sth_realized_price_usd',
        'lth_realized_price_usd',
      ],
      series.map(p => [
        p.date,
        p.close_usd,
        p.sma200 ?? '',
        p.wma200 ?? '',
        p.wma200_monthly_pct ?? '',
        p.mayer ?? '',
        p.drawdown_pct,
        p.cm_mvrv ?? '',
        p.biglens_mvrv ?? '',
        p.mvrv_delta_pct ?? '',
        p.puell_cm ?? '',
        p.puell_chain ?? '',
        p.realized_price_usd ?? '',
        p.sth_realized_price_usd ?? '',
        p.lth_realized_price_usd ?? '',
      ]),
    );
  };

  return (
    <div className="space-y-6">
      {poll.error && <ErrorBanner message={poll.error} />}

      {/* Range selector + Source status strip */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="inline-flex rounded-lg border border-zinc-800 bg-zinc-900/80 p-0.5 text-xs">
          {RANGES.map(r => (
            <button
              key={r.id}
              type="button"
              onClick={() => setRange(r.id)}
              className={`rounded-md px-3 py-1 font-medium transition ${
                range === r.id
                  ? 'bg-amber-500 text-zinc-950'
                  : 'text-zinc-400 hover:text-zinc-200'
              }`}
            >
              {r.label}
            </button>
          ))}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {data?.sources?.map(src => (
            <SourceBadge key={src.id} source={src} />
          ))}
        </div>
      </div>

      {/* KPI Row */}
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-6">
        <ValuationCard
          label="Daily Close / ATH"
          value={fmtUSD(latest?.close_usd)}
          sub={
            latest?.ath_usd != null
              ? `ATH ${fmtUSD(latest.ath_usd)} (${latest.ath_date ?? '—'})`
              : 'Warming up price history'
          }
        />
        <ValuationCard
          label="Drawdown / 200WMA"
          value={fmtPct(latest?.drawdown_pct, 1)}
          sub={
            latest?.wma200 != null
              ? `200WMA ${fmtUSD(latest.wma200)} (${fmtPct(latest.wma200_monthly_pct, 1, true)}/4w)`
              : 'Needs 1,400 daily closes'
          }
        />
        <ValuationCard
          label="Mayer Multiple"
          value={latest?.mayer != null ? `${latest.mayer.toFixed(2)}×` : '—'}
          sub={
            latest?.mayer_pctl_since_2011 != null
              ? `${latest.mayer_pctl_since_2011.toFixed(0)}th pctl since 2011 · 200DMA ${fmtUSD(latest.sma200)}`
              : 'Close ÷ 200D SMA'
          }
        />
        <ValuationCard
          label="MVRV Ratio"
          value={latest?.cm_mvrv != null ? `${latest.cm_mvrv.toFixed(2)}×` : '—'}
          sub={
            showBQ && latest?.biglens_mvrv != null
              ? `BigLens ${latest.biglens_mvrv.toFixed(2)}× (${fmtPct(latest.mvrv_delta_pct, 2, true)} vs CM)`
              : latest?.cm_mvrv_pctl_since_2011 != null
                ? `${latest.cm_mvrv_pctl_since_2011.toFixed(0)}th pctl since 2011 (Coin Metrics)`
                : 'Coin Metrics CapMVRVCur'
          }
        />
        <ValuationCard
          label="Puell Multiple"
          value={latest?.puell_cm != null ? `${latest.puell_cm.toFixed(2)}×` : '—'}
          sub={
            showBQ && latest?.puell_chain != null
              ? `Chain subsidy Puell: ${latest.puell_chain.toFixed(2)}×`
              : 'Daily issuance USD ÷ 365DMA'
          }
        />
        <ValuationCard
          label="Fear & Greed / Cycle"
          value={
            latest?.fear_greed_value != null
              ? `${latest.fear_greed_value} (${latest.fear_greed_class})`
              : '—'
          }
          sub={
            latest?.halving_cycle_day != null
              ? `Epoch 4 · Day ${latest.halving_cycle_day} since 2024-04-20`
              : 'alternative.me (365d)'
          }
        />
      </div>

      {/* Price & Trend (Log Scale) + 200WMA Heatmap + Realized Prices */}
      <Panel
        title={
          <span className="inline-flex items-center gap-2">
            <span>Price, 200DMA &amp; 200WMA Heatmap (Log Scale)</span>
            <HelpTip label="Price, 200WMA Heatmap & Realized Price">
              Log-scale daily BTC close with the 200-day SMA, 200-week MA (1,400 days), and a 200WMA
              heatmap colored by the 28-day percentage growth of the 200WMA. When BigQuery panels are
              shown and price coverage is complete since 2010-08-18, BigLens UTXO Realized Price, STH
              Realized Price (&lt;155d), and LTH Realized Price (≥155d) are overlaid.
            </HelpTip>
            <button
              type="button"
              onClick={exportSeriesCsv}
              className="ml-2 inline-flex items-center gap-1 rounded border border-zinc-700 bg-zinc-800/80 px-2 py-0.5 text-[11px] font-normal text-zinc-300 hover:border-zinc-600 hover:text-zinc-100"
            >
              <Download className="h-3 w-3" />
              <span>CSV</span>
            </button>
          </span>
        }
        note={
          showBQ && latest?.realized_price_blocked
            ? `Realized prices gated: ${latest.realized_price_blocked}`
            : showBQ && latest?.realized_price_usd
              ? `Realized ${fmtUSD(latest.realized_price_usd)} · STH ${fmtUSD(latest.sth_realized_price_usd)} · LTH ${fmtUSD(latest.lth_realized_price_usd)}`
              : 'Log10 USD · 200WMA dots colored by 4-week % change'
        }
      >
        {priceTrendOpt ? (
          <ReactECharts option={priceTrendOpt} style={{ height: 360 }} notMerge />
        ) : (
          <EmptyState text={data?.warming_up ? 'Warming up historical daily closes...' : 'No price series available'} />
        )}
      </Panel>

      {/* Drawdown + Halving Cycles */}
      <div className="grid gap-6 lg:grid-cols-2">
        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              <span>Drawdown from Running All-Time High</span>
              <HelpTip label="Drawdown from ATH">
                Percentage decline from the highest daily close observed up to each date:
                (Close_t / max_{'{s ≤ t}'} Close_s − 1) × 100.
              </HelpTip>
            </span>
          }
          note={
            latest?.ath_usd != null
              ? `Current ${fmtPct(latest.drawdown_pct, 1)} vs ATH ${fmtUSD(latest.ath_usd)}`
              : 'Daily close drawdown (%)'
          }
        >
          {drawdownOpt ? (
            <ReactECharts option={drawdownOpt} style={{ height: 270 }} notMerge />
          ) : (
            <EmptyState text="No drawdown series available" />
          )}
        </Panel>

        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              <span>Halving Cycles Overlay (Indexed = 1.0× at Halving)</span>
              <HelpTip label="Halving Cycles Overlay">
                Compares Bitcoin&apos;s four post-halving epochs (2012-11-28, 2016-07-09, 2020-05-11,
                2024-04-20) on a logarithmic scale, with each epoch&apos;s halving-day close indexed
                to 1.0×.
              </HelpTip>
            </span>
          }
          note={
            latest?.halving_cycle_day != null
              ? `Epoch 4 (2024–) is at day ${latest.halving_cycle_day}`
              : 'Log10 multiple vs days since halving'
          }
        >
          {halvingCyclesOpt ? (
            <ReactECharts option={halvingCyclesOpt} style={{ height: 270 }} notMerge />
          ) : (
            <EmptyState text="No halving cycle series available" />
          )}
        </Panel>
      </div>

      {/* Mayer Multiple + MVRV */}
      <div className="grid gap-6 lg:grid-cols-2">
        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              <span>Mayer Multiple (Close ÷ 200D SMA)</span>
              <HelpTip label="Mayer Multiple">
                Ratio of daily BTC close to its 200-day simple moving average. Historical reference
                bands: 0.8× (deep value / accumulation) and 2.4× (cycle overheating). Percentile rank
                is computed over all daily observations since 2011-01-01.
              </HelpTip>
            </span>
          }
          note={
            latest?.mayer != null && latest?.mayer_pctl_since_2011 != null
              ? `Latest ${latest.mayer.toFixed(2)}× (${latest.mayer_pctl_since_2011.toFixed(1)}th percentile since 2011)`
              : '0.8× accumulation · 2.4× overheated'
          }
        >
          {mayerOpt ? (
            <ReactECharts option={mayerOpt} style={{ height: 270 }} notMerge />
          ) : (
            <EmptyState text="Needs at least 200 daily closes to compute Mayer Multiple" />
          )}
        </Panel>

        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              <span>MVRV Ratio (Market Cap ÷ Realized Cap)</span>
              <HelpTip label="MVRV Ratio">
                Coin Metrics Community CapMVRVCur (CC BY-NC 4.0) alongside BigLens&apos;s UTXO-derived
                MVRV when BigQuery panels are enabled and a UTXO baseline is present. Shows the
                percentage divergence Δ% between Coin Metrics and BigLens on-chain accounting.
              </HelpTip>
            </span>
          }
          note={
            showBQ && latest?.biglens_mvrv != null
              ? `CM ${latest?.cm_mvrv?.toFixed(2) ?? '—'}× · BigLens ${latest.biglens_mvrv.toFixed(2)}× (Δ ${fmtPct(latest.mvrv_delta_pct, 2, true)})`
              : 'Coin Metrics Community (CC BY-NC 4.0)'
          }
        >
          {mvrvOpt ? (
            <ReactECharts option={mvrvOpt} style={{ height: 270 }} notMerge />
          ) : (
            <EmptyState text="No MVRV series available" />
          )}
        </Panel>
      </div>

      {/* Puell Multiple + Fear & Greed */}
      <div className="grid gap-6 lg:grid-cols-2">
        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              <span>Puell Multiple (Daily Issuance USD ÷ 365D MA)</span>
              <HelpTip label="Puell Multiple">
                Measures miner supply pressure by dividing daily USD issuance by its 365-day moving
                average. When BigQuery panels are shown, exact chain subsidy issuance (Σ per-block
                subsidy_sats from Job A2) is plotted alongside Coin Metrics IssTotNtv.
              </HelpTip>
            </span>
          }
          note={
            showBQ && latest?.puell_chain != null
              ? `CM ${latest?.puell_cm?.toFixed(2) ?? '—'}× · Chain ${latest.puell_chain.toFixed(2)}×`
              : '0.5× miner stress · 4.0× issuance peak'
          }
        >
          {puellOpt ? (
            <ReactECharts option={puellOpt} style={{ height: 270 }} notMerge />
          ) : (
            <EmptyState text="Needs 365 days of issuance history for Puell Multiple" />
          )}
        </Panel>

        <Panel
          title={
            <span className="inline-flex items-center gap-1.5">
              <span>Crypto Fear &amp; Greed Index (365 Days)</span>
              <HelpTip label="Fear & Greed Index">
                Daily composite sentiment score (0 = Extreme Fear, 100 = Extreme Greed) provided by
                alternative.me.
              </HelpTip>
            </span>
          }
          note={
            latest?.fear_greed_value != null
              ? `Latest: ${latest.fear_greed_value} (${latest.fear_greed_class}) · Source: alternative.me`
              : 'Source: alternative.me (attribution required)'
          }
        >
          {fngOpt ? (
            <ReactECharts option={fngOpt} style={{ height: 270 }} notMerge />
          ) : (
            <EmptyState text="Fear & Greed history unavailable" />
          )}
        </Panel>
      </div>

      {/* BigQuery Locked / On-Chain Valuation Attribution */}
      {!showBQ && (
        <BigQueryLocked
          title="On-Chain Realized Price, STH/LTH Cost Basis & Exact Puell Multiple"
          description="Enable BigQuery panels to overlay UTXO-derived Realized Price, Short-Term / Long-Term Holder Realized Prices, BigLens MVRV (with Δ% cross-check against Coin Metrics), and exact per-block subsidy Puell Multiple."
          bigquery={bigquery}
          onEnableDisplay={bqControl.available ? bqControl.toggle : undefined}
        />
      )}
    </div>
  );
}

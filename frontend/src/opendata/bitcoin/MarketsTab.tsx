import ReactECharts from 'echarts-for-react';
import { AlertCircle, Download, Info } from 'lucide-react';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import { AXIS_LABEL, CHART_TOOLTIP, Panel, SPLIT_LINE } from '../crypto/shared';
import HelpTip from '../crypto/HelpTip';
import { fetchBitcoinMarkets } from './api';
import { downloadCsv, escapeHtml, fmtPct, fmtUSD } from '../common/format';
import { fmtBTC } from './format';
import { usePolling } from '../common/usePolling';
import SourceBadge from '../common/SourceBadge';
import type { BtcVenueRow } from './types';

const VENUE_LABELS: Record<string, string> = {
  binance: 'Binance',
  okx: 'OKX',
  coinbase: 'Coinbase (Spot / INTX / CDE)',
  kraken: 'Kraken',
  hyperliquid: 'Hyperliquid',
  deribit: 'Deribit',
  bybit: 'Bybit',
  cme: 'CME (CFTC)',
};

const KIND_LABELS: Record<string, { label: string; cls: string }> = {
  cex_offshore: {
    label: 'CEX Offshore',
    cls: 'border-sky-500/30 bg-sky-500/10 text-sky-300',
  },
  cex_us: {
    label: 'CEX US',
    cls: 'border-indigo-500/30 bg-indigo-500/10 text-indigo-300',
  },
  dex: {
    label: 'On-Chain DEX',
    cls: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300',
  },
  regulated_futures: {
    label: 'US Regulated Futures',
    cls: 'border-amber-500/30 bg-amber-500/10 text-amber-300',
  },
};

const isViaCoinGecko = (row: BtcVenueRow): boolean =>
  row.sources.length > 0 && row.sources[0].via === 'coingecko';

export default function MarketsTab({ refreshTick }: { refreshTick: number }) {
  const poll = usePolling(fetchBitcoinMarkets, 60_000, `btc-markets-${refreshTick}`);
  const data = poll.data;

  if (poll.error && !data) {
    return <ErrorBanner message={poll.error} />;
  }
  if (!data && poll.loading) {
    return (
      <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-8 text-center text-xs text-zinc-400">
        Loading Bitcoin spot &amp; derivatives market structure…
      </div>
    );
  }
  if (!data) {
    return <EmptyState text="No Bitcoin market structure data available." />;
  }

  const venues = data.venues ?? [];
  const oiShares = data.oi_shares ?? [];
  const perVenueRatios = data.ratios?.per_venue ?? [];
  const basis = data.basis ?? [];
  const dvol = data.dvol ?? [];
  const cmeHistory = data.cot?.cme_history ?? [];
  const history = data.history ?? [];
  const okxRubik = data.okx_rubik ?? [];

  const handleExportVenues = () => {
    downloadCsv(
      'bitcoin_markets_venues.csv',
      [
        'venue',
        'kind',
        'weekly',
        'via',
        'spot_24h_usd',
        'depth_2pct_usd',
        'spread_pct',
        'deriv_24h_usd',
        'oi_usd',
        'oi_btc',
        'funding_8h_pct',
        'funding_apr_pct',
        'premium_pct',
      ],
      venues.map(v => [
        v.venue,
        v.kind,
        v.weekly ? 'true' : 'false',
        v.sources[0]?.via ?? 'direct',
        v.spot_24h_usd,
        v.depth_2pct_usd,
        v.spread_pct,
        v.deriv_24h_usd,
        v.oi_usd,
        v.oi_btc,
        v.funding_8h_pct,
        v.funding_apr_pct,
        v.premium_pct,
      ]),
    );
  };

  // ECharts option: Open Interest Share by Venue
  const oiShareOption = {
    tooltip: {
      ...CHART_TOOLTIP,
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
    },
    grid: { left: 110, right: 28, top: 16, bottom: 28 },
    xAxis: {
      type: 'value',
      axisLabel: {
        ...AXIS_LABEL,
        formatter: (v: number) => `${v.toFixed(0)}%`,
      },
      splitLine: SPLIT_LINE,
    },
    yAxis: {
      type: 'category',
      inverse: true,
      data: oiShares.map(
        s =>
          `${VENUE_LABELS[s.venue] ?? s.venue}${s.weekly ? ' (wk)' : ''}${
            s.via === 'coingecko' ? ' *' : ''
          }`,
      ),
      axisLabel: AXIS_LABEL,
    },
    series: [
      {
        name: 'OI Share %',
        type: 'bar',
        data: oiShares.map(s => Number(s.share_pct.toFixed(2))),
        itemStyle: {
          color: (params: { dataIndex: number }) => {
            const s = oiShares[params.dataIndex];
            if (!s) return '#f59e0b';
            if (s.kind === 'dex') return '#10b981';
            if (s.kind === 'regulated_futures' || s.kind === 'cex_us') return '#6366f1';
            return '#f59e0b';
          },
          borderRadius: [0, 4, 4, 0],
        },
      },
    ],
  };

  // ECharts option: Annualized Basis Curve
  const basisOption = {
    tooltip: {
      ...CHART_TOOLTIP,
      trigger: 'item',
      formatter: (p: { data: [number, number, string, string, number] }) => {
        const [days, apr, inst, venue, futPx] = p.data;
        return `<div class="text-xs"><b>${escapeHtml(inst)}</b> (${escapeHtml(venue)})<br/>Days to expiry: ${days.toFixed(
          1,
        )}d<br/>Annualized basis: <b>${apr.toFixed(2)}% APR</b><br/>Future: ${fmtUSD(futPx)}</div>`;
      },
    },
    grid: { left: 52, right: 24, top: 20, bottom: 36 },
    xAxis: {
      type: 'value',
      name: 'Days to Expiry',
      nameLocation: 'middle',
      nameGap: 24,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    yAxis: {
      type: 'value',
      name: 'Basis APR %',
      axisLabel: {
        ...AXIS_LABEL,
        formatter: (v: number) => `${v.toFixed(1)}%`,
      },
      splitLine: SPLIT_LINE,
    },
    series: [
      {
        name: 'Dated Futures',
        type: 'line',
        symbolSize: 9,
        data: basis.map(b => [
          Number(b.days_to_expiry.toFixed(2)),
          Number(b.basis_apr_pct.toFixed(2)),
          b.instrument,
          b.venue,
          b.future_price,
        ]),
        itemStyle: { color: '#38bdf8' },
        lineStyle: { width: 2, color: '#38bdf8' },
      },
    ],
  };

  // ECharts option: CME Positioning (CFTC TFF, 52 weeks)
  const cmeOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 11 },
    },
    grid: { left: 64, right: 64, top: 32, bottom: 28 },
    xAxis: {
      type: 'category',
      data: cmeHistory.map(r => r.report_date),
      axisLabel: AXIS_LABEL,
    },
    yAxis: [
      {
        type: 'value',
        name: 'Net BTC',
        axisLabel: AXIS_LABEL,
        splitLine: SPLIT_LINE,
      },
      {
        type: 'value',
        name: 'Total OI (BTC)',
        axisLabel: AXIS_LABEL,
        splitLine: { show: false },
      },
    ],
    series: [
      {
        name: 'Asset Managers Net (BTC)',
        type: 'bar',
        stack: 'pos',
        data: cmeHistory.map(r => Math.round(r.asset_managers_net_btc)),
        itemStyle: { color: '#10b981' },
      },
      {
        name: 'Leveraged Funds Net (BTC)',
        type: 'bar',
        stack: 'pos',
        data: cmeHistory.map(r => Math.round(r.leveraged_funds_net_btc)),
        itemStyle: { color: '#f43f5e' },
      },
      {
        name: 'Dealers Net (BTC)',
        type: 'bar',
        stack: 'pos',
        data: cmeHistory.map(r => Math.round(r.dealers_net_btc)),
        itemStyle: { color: '#f59e0b' },
      },
      {
        name: 'CME Open Interest (BTC)',
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        showSymbol: false,
        data: cmeHistory.map(r => Math.round(r.open_interest_btc)),
        itemStyle: { color: '#a855f7' },
        lineStyle: { width: 2, color: '#a855f7' },
      },
    ],
  };

  // ECharts option: DVOL History
  const dvolOption = {
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    grid: { left: 48, right: 20, top: 20, bottom: 28 },
    xAxis: {
      type: 'category',
      data: dvol.map(d => d.date),
      axisLabel: AXIS_LABEL,
    },
    yAxis: {
      type: 'value',
      axisLabel: {
        ...AXIS_LABEL,
        formatter: (v: number) => `${v.toFixed(0)}%`,
      },
      splitLine: SPLIT_LINE,
    },
    series: [
      {
        name: 'Deribit DVOL (Close %)',
        type: 'line',
        smooth: true,
        showSymbol: false,
        data: dvol.map(d => Number(d.close.toFixed(2))),
        itemStyle: { color: '#f59e0b' },
        areaStyle: { color: 'rgba(245, 158, 11, 0.12)' },
      },
    ],
  };

  // ECharts option: OKX Rubik / Daily Market Snapshot History
  const historyOption =
    history.length > 0
      ? {
          tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
          legend: { top: 0, textStyle: { color: '#a1a1aa', fontSize: 11 } },
          grid: { left: 52, right: 52, top: 30, bottom: 28 },
          xAxis: {
            type: 'category',
            data: history.map(h => h.date),
            axisLabel: AXIS_LABEL,
          },
          yAxis: [
            {
              type: 'value',
              name: 'Deriv/Spot (x)',
              axisLabel: AXIS_LABEL,
              splitLine: SPLIT_LINE,
            },
            {
              type: 'value',
              name: 'Share %',
              axisLabel: {
                ...AXIS_LABEL,
                formatter: (v: number) => `${v.toFixed(0)}%`,
              },
              splitLine: { show: false },
            },
          ],
          series: [
            {
              name: 'Curated Deriv/Spot Ratio',
              type: 'line',
              data: history.map(h =>
                h.major_deriv_spot !== null ? Number(h.major_deriv_spot.toFixed(2)) : null,
              ),
              itemStyle: { color: '#f59e0b' },
            },
            {
              name: 'DEX OI Share %',
              type: 'line',
              yAxisIndex: 1,
              data: history.map(h =>
                h.dex_share_pct !== null ? Number(h.dex_share_pct.toFixed(2)) : null,
              ),
              itemStyle: { color: '#10b981' },
            },
            {
              name: 'US-Regulated OI Share %',
              type: 'line',
              yAxisIndex: 1,
              data: history.map(h =>
                h.us_regulated_share_pct !== null
                  ? Number(h.us_regulated_share_pct.toFixed(2))
                  : null,
              ),
              itemStyle: { color: '#6366f1' },
            },
          ],
        }
      : {
          tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
          legend: { top: 0, textStyle: { color: '#a1a1aa', fontSize: 11 } },
          grid: { left: 60, right: 60, top: 30, bottom: 28 },
          xAxis: {
            type: 'category',
            data: okxRubik.map(p => p.date),
            axisLabel: AXIS_LABEL,
          },
          yAxis: [
            {
              type: 'value',
              name: 'OKX OI ($B)',
              axisLabel: {
                ...AXIS_LABEL,
                formatter: (v: number) => `$${(v / 1e9).toFixed(1)}B`,
              },
              splitLine: SPLIT_LINE,
            },
            {
              type: 'value',
              name: '24h Vol ($B)',
              axisLabel: {
                ...AXIS_LABEL,
                formatter: (v: number) => `$${(v / 1e9).toFixed(0)}B`,
              },
              splitLine: { show: false },
            },
          ],
          series: [
            {
              name: 'OKX Contract OI (USD)',
              type: 'line',
              showSymbol: false,
              data: okxRubik.map(p => Math.round(p.oi_usd)),
              itemStyle: { color: '#38bdf8' },
            },
            {
              name: 'OKX Contract Volume (USD)',
              type: 'bar',
              yAxisIndex: 1,
              data: okxRubik.map(p => Math.round(p.volume_24h_usd)),
              itemStyle: { color: 'rgba(245, 158, 11, 0.35)' },
            },
          ],
        };

  return (
    <div className="space-y-6">
      {/* KPI / Market Structure Ratio Strip */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
        <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3.5">
          <div className="flex items-center justify-between text-[11px] text-zinc-400">
            <span>Deriv / Spot Ratio (Curated)</span>
            <HelpTip label="Curated derivatives / spot ratio">
              Σ curated derivatives 24h volume (Binance, OKX, Coinbase, Kraken, Hyperliquid, Deribit, Bybit) ÷ Σ curated
              spot 24h volume (excluding anomaly/stale tickers).
            </HelpTip>
          </div>
          <div className="mt-1 text-lg font-bold text-amber-300">
            {data.ratios.major_deriv_spot !== null
              ? `${data.ratios.major_deriv_spot.toFixed(2)}×`
              : '—'}
          </div>
          <div className="mt-0.5 text-[11px] text-zinc-500">
            {fmtUSD(data.ratios.curated_deriv_24h_usd)} vs {fmtUSD(data.ratios.curated_spot_24h_usd)}{' '}
            spot
          </div>
        </div>

        <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3.5">
          <div className="flex items-center justify-between text-[11px] text-zinc-400">
            <span>All-Reported Deriv / Spot</span>
            <HelpTip label="All-reported derivatives / spot ratio">
              CoinGecko all-reported BTC-index derivatives volume ÷ all-reported BTC spot volume. Labeled unverified
              because long-tail venues may include wash trading.
            </HelpTip>
          </div>
          <div className="mt-1 flex items-baseline gap-1.5">
            <span className="text-lg font-bold text-zinc-200">
              {data.ratios.all_reported_deriv_spot !== null
                ? `${data.ratios.all_reported_deriv_spot.toFixed(2)}×`
                : '—'}
            </span>
            <span className="rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[10px] text-amber-300">
              unverified
            </span>
          </div>
          <div className="mt-0.5 text-[11px] text-zinc-500">
            {fmtUSD(data.ratios.all_reported_deriv_24h_usd)} vs{' '}
            {fmtUSD(data.ratios.all_reported_spot_24h_usd)}
          </div>
        </div>

        <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3.5">
          <div className="flex items-center justify-between text-[11px] text-zinc-400">
            <span>Total Open Interest &amp; OI/Mcap</span>
            <HelpTip label="Total OI & OI/market cap">
              Σ curated BTC open interest (including weekly CME from CFTC) and its ratio to Bitcoin market cap (leverage
              proxy).
            </HelpTip>
          </div>
          <div className="mt-1 text-lg font-bold text-zinc-100">{fmtUSD(data.total_oi_usd)}</div>
          <div className="mt-0.5 text-[11px] text-zinc-500">
            OI / Market Cap: <span className="text-zinc-300">{fmtPct(data.oi_to_mcap_pct, 2)}</span>
          </div>
        </div>

        <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3.5">
          <div className="flex items-center justify-between text-[11px] text-zinc-400">
            <span>On-Chain DEX Share</span>
            <HelpTip label="On-chain DEX share">
              Hyperliquid BTC perp open interest ÷ Σ curated perpetual/futures open interest (excluding weekly CME).
            </HelpTip>
          </div>
          <div className="mt-1 text-lg font-bold text-emerald-300">
            {fmtPct(data.dex_share_pct, 2)}
          </div>
          <div className="mt-0.5 text-[11px] text-zinc-500">Hyperliquid vs CEX perps</div>
        </div>

        <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3.5">
          <div className="flex items-center justify-between text-[11px] text-zinc-400">
            <span>US-Regulated OI Share</span>
            <HelpTip label="US-regulated OI share">
              (CME weekly CFTC OI + Coinbase CDE OI) ÷ Σ curated OI including CME.
            </HelpTip>
          </div>
          <div className="mt-1 flex items-baseline gap-1.5">
            <span className="text-lg font-bold text-indigo-300">
              {fmtPct(data.us_regulated_share_pct, 2)}
            </span>
            <span className="text-[10px] text-zinc-400">CME weekly + CDE</span>
          </div>
          <div className="mt-0.5 text-[11px] text-zinc-500">
            CFTC as of {data.cot?.as_of_report_date || 'latest'}
          </div>
        </div>

        <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3.5">
          <div className="flex items-center justify-between text-[11px] text-zinc-400">
            <span>OI-Wtd Funding &amp; Vol</span>
            <HelpTip label="OI-weighted funding & volatility">
              OI-weighted annualized funding APR across direct venues (Σ APRᵢ·OIᵢ / Σ OIᵢ), plus Deribit DVOL vs 30-day
              realized volatility (VRP = DVOL − RV30).
            </HelpTip>
          </div>
          <div className="mt-1 text-lg font-bold text-sky-300">
            {fmtPct(data.funding_oi_weighted_apr, 2, true)} APR
          </div>
          <div className="mt-0.5 text-[11px] text-zinc-500">
            DVOL {fmtPct(data.dvol_latest, 1)} · RV30 {fmtPct(data.rv30, 1)} · VRP{' '}
            {fmtPct(data.vrp, 1, true)}
          </div>
        </div>
      </div>

      {data.ratios.note && (
        <div className="flex items-center gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3.5 py-2 text-xs text-amber-200">
          <AlertCircle className="h-4 w-4 shrink-0 text-amber-400" />
          <span>{data.ratios.note}</span>
        </div>
      )}

      {/* Venue Board */}
      <Panel
        title={
          <span className="inline-flex items-center gap-2">
            <span>Bitcoin Venue Board — Spot &amp; Derivatives Structure</span>
            <button
              type="button"
              onClick={handleExportVenues}
              className="inline-flex items-center gap-1 rounded-md border border-zinc-700/80 bg-zinc-800/70 px-2 py-0.5 text-[11px] font-normal text-zinc-300 hover:text-zinc-100"
            >
              <Download className="h-3 w-3" />
              <span>CSV</span>
            </button>
          </span>
        }
        note="Normalized USD notional volumes, ±2% depth, OI (USD & BTC), and 8h/APR funding"
      >
        <div className="overflow-x-auto">
          <table className="w-full text-left text-xs">
            <thead>
              <tr className="border-b border-zinc-800 text-[11px] text-zinc-400">
                <th className="py-2 pr-3 font-medium">Venue</th>
                <th className="px-2 py-2 font-medium">Type</th>
                <th className="px-2 py-2 text-right font-medium">Spot 24h Vol</th>
                <th className="px-2 py-2 text-right font-medium">±2% Depth</th>
                <th className="px-2 py-2 text-right font-medium">Spread</th>
                <th className="px-2 py-2 text-right font-medium">Perp/Fut 24h Vol</th>
                <th className="px-2 py-2 text-right font-medium">Open Interest (USD)</th>
                <th className="px-2 py-2 text-right font-medium">Open Interest (BTC)</th>
                <th className="px-2 py-2 text-right font-medium">Funding (8h)</th>
                <th className="px-2 py-2 text-right font-medium">Funding APR</th>
                <th className="px-2 py-2 text-right font-medium">Premium</th>
                <th className="pl-3 py-2 font-medium">Source</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-800/60">
              {venues.map(row => {
                const viaCG = isViaCoinGecko(row);
                const kindMeta = KIND_LABELS[row.kind] ?? {
                  label: row.kind,
                  cls: 'border-zinc-700 bg-zinc-800 text-zinc-300',
                };
                const derivCellClass = viaCG ? 'italic text-amber-200/90' : 'text-zinc-200';
                const derivTooltip = viaCG
                  ? 'Served via CoinGecko derivatives fallback (direct venue API unavailable or geo-restricted from server region)'
                  : undefined;

                return (
                  <tr key={row.venue} className="hover:bg-zinc-800/30">
                    <td className="py-2.5 pr-3 font-medium text-zinc-100">
                      <div className="flex items-center gap-1.5">
                        <span>{VENUE_LABELS[row.venue] ?? row.venue}</span>
                        {row.weekly && (
                          <span
                            title="Reported weekly by CFTC Commitments of Traders (as of Tuesday)"
                            className="rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-normal text-amber-300"
                          >
                            weekly
                          </span>
                        )}
                      </div>
                    </td>
                    <td className="px-2 py-2.5">
                      <span
                        className={`inline-block rounded-full border px-2 py-0.5 text-[10px] font-medium ${kindMeta.cls}`}
                      >
                        {kindMeta.label}
                      </span>
                    </td>
                    <td className="px-2 py-2.5 text-right font-mono text-zinc-200">
                      {fmtUSD(row.spot_24h_usd)}
                    </td>
                    <td className="px-2 py-2.5 text-right font-mono text-zinc-300">
                      {fmtUSD(row.depth_2pct_usd)}
                    </td>
                    <td className="px-2 py-2.5 text-right font-mono text-zinc-400">
                      {fmtPct(row.spread_pct, 3)}
                    </td>
                    <td
                      title={derivTooltip}
                      className={`px-2 py-2.5 text-right font-mono ${derivCellClass}`}
                    >
                      {fmtUSD(row.deriv_24h_usd)}
                    </td>
                    <td
                      title={derivTooltip}
                      className={`px-2 py-2.5 text-right font-mono ${derivCellClass}`}
                    >
                      {fmtUSD(row.oi_usd)}
                    </td>
                    <td
                      title={derivTooltip}
                      className={`px-2 py-2.5 text-right font-mono ${derivCellClass}`}
                    >
                      {fmtBTC(row.oi_btc, 0)}
                    </td>
                    <td
                      title={derivTooltip}
                      className={`px-2 py-2.5 text-right font-mono ${derivCellClass}`}
                    >
                      {fmtPct(row.funding_8h_pct, 4, true)}
                    </td>
                    <td
                      title={derivTooltip}
                      className={`px-2 py-2.5 text-right font-mono ${derivCellClass}`}
                    >
                      {fmtPct(row.funding_apr_pct, 2, true)}
                    </td>
                    <td className="px-2 py-2.5 text-right font-mono text-zinc-300">
                      {fmtPct(row.premium_pct, 3, true)}
                    </td>
                    <td className="pl-3 py-2.5">
                      <div className="flex flex-wrap gap-1">
                        {row.sources.map((s, idx) => (
                          <SourceBadge
                            key={`${row.venue}-${s.id}-${idx}`}
                            source={s}
                            weekly={row.weekly && idx === 0}
                          />
                        ))}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </Panel>

      {/* OI Share Bar + Per-Venue Ratio & Liquidity */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <Panel
          title="Open Interest Distribution by Venue"
          note="Curated BTC OI share (* = via CoinGecko, wk = CFTC weekly)"
        >
          {oiShares.length > 0 ? (
            <ReactECharts option={oiShareOption} style={{ height: 280 }} />
          ) : (
            <EmptyState text="Open interest distribution unavailable." />
          )}
        </Panel>

        <Panel
          title="Per-Venue Derivatives / Spot Ratio & Liquidity"
          note="Leverage concentration vs spot volume and ±2% depth"
        >
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead>
                <tr className="border-b border-zinc-800 text-[11px] text-zinc-400">
                  <th className="py-2 pr-3 font-medium">Venue</th>
                  <th className="px-2 py-2 text-right font-medium">Spot 24h</th>
                  <th className="px-2 py-2 text-right font-medium">Perp/Fut 24h</th>
                  <th className="px-2 py-2 text-right font-medium">Deriv / Spot</th>
                  <th className="px-2 py-2 text-right font-medium">±2% Depth</th>
                  <th className="pl-2 py-2 text-right font-medium">Spread</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/60">
                {perVenueRatios.map(r => {
                  const vRow = venues.find(v => v.venue === r.venue);
                  return (
                    <tr key={r.venue} className="hover:bg-zinc-800/30">
                      <td className="py-2 pr-3 font-medium text-zinc-100">
                        {VENUE_LABELS[r.venue] ?? r.venue}
                      </td>
                      <td className="px-2 py-2 text-right font-mono text-zinc-300">
                        {fmtUSD(r.spot_24h_usd)}
                      </td>
                      <td className="px-2 py-2 text-right font-mono text-zinc-300">
                        {fmtUSD(r.deriv_24h_usd)}
                      </td>
                      <td className="px-2 py-2 text-right font-mono font-semibold text-amber-300">
                        {r.ratio !== null ? `${r.ratio.toFixed(2)}×` : '—'}
                      </td>
                      <td className="px-2 py-2 text-right font-mono text-zinc-300">
                        {fmtUSD(vRow?.depth_2pct_usd)}
                      </td>
                      <td className="pl-2 py-2 text-right font-mono text-zinc-400">
                        {fmtPct(vRow?.spread_pct, 3)}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Panel>
      </div>

      {/* Basis Curve + Funding Table */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <Panel
          title="Annualized Futures Basis Curve"
          note="Deribit & Kraken dated futures: (F/S − 1) × 365 / DaysToExpiry"
        >
          {basis.length > 0 ? (
            <ReactECharts option={basisOption} style={{ height: 270 }} />
          ) : (
            <EmptyState text="Dated futures basis points unavailable." />
          )}
        </Panel>

        <Panel
          title="Perpetual Funding Rates & Carry"
          note="Normalized 8h funding rate and annualized APR"
        >
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead>
                <tr className="border-b border-zinc-800 text-[11px] text-zinc-400">
                  <th className="py-2 pr-3 font-medium">Venue</th>
                  <th className="px-2 py-2 text-right font-medium">Open Interest</th>
                  <th className="px-2 py-2 text-right font-medium">Funding (8h)</th>
                  <th className="px-2 py-2 text-right font-medium">Funding APR</th>
                  <th className="pl-2 py-2 text-right font-medium">Feed</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800/60">
                {venues
                  .filter(v => v.venue !== 'cme')
                  .map(v => {
                    const viaCG = isViaCoinGecko(v);
                    return (
                      <tr key={v.venue} className="hover:bg-zinc-800/30">
                        <td className="py-2 pr-3 font-medium text-zinc-100">
                          {VENUE_LABELS[v.venue] ?? v.venue}
                        </td>
                        <td className="px-2 py-2 text-right font-mono text-zinc-300">
                          {fmtUSD(v.oi_usd)}
                        </td>
                        <td
                          className={`px-2 py-2 text-right font-mono ${
                            viaCG ? 'italic text-amber-200/90' : 'text-zinc-200'
                          }`}
                        >
                          {fmtPct(v.funding_8h_pct, 4, true)}
                        </td>
                        <td
                          className={`px-2 py-2 text-right font-mono font-medium ${
                            viaCG ? 'italic text-amber-200/90' : 'text-sky-300'
                          }`}
                        >
                          {fmtPct(v.funding_apr_pct, 2, true)}
                        </td>
                        <td className="pl-2 py-2 text-right text-[11px] text-zinc-400">
                          {viaCG ? <span className="italic">via CoinGecko</span> : 'direct'}
                        </td>
                      </tr>
                    );
                  })}
              </tbody>
            </table>
          </div>
        </Panel>
      </div>

      {/* CME CFTC Positioning + Volatility / History */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <Panel
          title="CME Institutional Positioning (CFTC TFF, Weekly)"
          note="Net positioning in BTC (5 BTC/contract): Asset Managers, Leveraged Funds, Dealers"
        >
          {cmeHistory.length > 0 ? (
            <ReactECharts option={cmeOption} style={{ height: 300 }} />
          ) : (
            <EmptyState text="CFTC Commitments of Traders history unavailable." />
          )}
        </Panel>

        <Panel
          title="Implied vs Realized Volatility (Deribit DVOL vs RV30)"
          note={`DVOL ${fmtPct(data.dvol_latest, 1)} vs RV30 ${fmtPct(data.rv30, 1)} · VRP ${fmtPct(
            data.vrp,
            1,
            true,
          )}`}
        >
          {dvol.length > 0 ? (
            <ReactECharts option={dvolOption} style={{ height: 300 }} />
          ) : (
            <EmptyState text="Deribit DVOL series unavailable." />
          )}
        </Panel>
      </div>

      {/* Market Structure History / OKX Rubik Reference */}
      <Panel
        title={
          history.length > 0
            ? 'Market Structure History (Daily Snapshots)'
            : 'Contract Open Interest & Volume History (OKX Rubik Reference)'
        }
        note={
          history.length > 0
            ? 'Daily snapshots accrued in local SQLite (btc_market_daily)'
            : 'OKX 180-day contract OI and volume reference series'
        }
      >
        {history.length > 0 || okxRubik.length > 0 ? (
          <ReactECharts option={historyOption} style={{ height: 280 }} />
        ) : (
          <EmptyState text="Market history series warming up." />
        )}
      </Panel>

      {/* Caveats Footer */}
      {data.caveats && data.caveats.length > 0 && (
        <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/40 p-4 text-xs text-zinc-400">
          <div className="mb-1.5 flex items-center gap-1.5 font-semibold text-zinc-300">
            <Info className="h-3.5 w-3.5 text-amber-400" />
            <span>Market Structure Notes &amp; Caveats</span>
          </div>
          <ul className="list-disc space-y-1 pl-5">
            {data.caveats.map((c, idx) => (
              <li key={idx}>{c}</li>
            ))}
          </ul>
        </div>
      )}

      {/* Sources Footer */}
      {data.sources && data.sources.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 border-t border-zinc-800/70 pt-3">
          <span className="text-[11px] font-medium text-zinc-500">Markets sources:</span>
          {data.sources.map((s, idx) => (
            <SourceBadge
              key={`${s.id}-${idx}`}
              source={s}
              weekly={s.id === 'cftc'}
            />
          ))}
        </div>
      )}
    </div>
  );
}

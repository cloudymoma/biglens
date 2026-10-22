import ReactECharts from 'echarts-for-react';
import { usePolling } from '../common/usePolling';
import { fetchStablePeg } from './api';
import { ASSET_COLOR, axisStyle, baseOption, chrome, colorOf, HEAT_BINS, tipHtml, withGaps } from './charts';
import type { TipRow } from './charts';
import type { StableAsset, StableRange } from './deeplink';
import { fmtBps, fmtDateTime, fmtEurcBool, fmtPrice, fmtTimeHM } from './format';
import SignalChip from './SignalChip';
import SignalMatrix from './SignalMatrix';
import type { Theme } from './signals';
import { cx } from './styles';
import { useStableTheme } from './theme';
import type { StableAnnotation, StableMatrixRow, StablePeg, StablePegAsset, StablePegHeatmap, StableProviderHealth } from './types';
import { ColdState, CsvButton, ErrorNote, ExtLink, KV, Panel, Stat } from './ui';
import VenueTable from './VenueTable';

// Peg & Liquidity (stablecoin.md §7.2, tab 2): consensus, venue inputs, the
// per-venue heatmap, the relative peg and Uniswap, EURC in euro terms, and
// the depeg history since 2020. Curve and Chainlink panels are v1.1.

const SYM: Record<string, string> = { usdt: 'USDT', usdc: 'USDC', eurc: 'EURC' };
const UNISWAP_POOL = 'https://app.uniswap.org/explore/pools/ethereum/0x3416cf6c708da44db2624d63ea0aaef7113527c6';

function sigState(matrix: StableMatrixRow[], asset: string, id: string): string | undefined {
  return matrix.find(r => r.asset === asset)?.signals.find(s => s.id === id)?.state;
}

interface PricePoint {
  value: [string, number | null];
  price: number | null;
  numeraire: string;
}

function ConsensusCard({ a, warmingUp, matrix }: { a: StablePegAsset; warmingUp: boolean; matrix: StableMatrixRow[] }) {
  return (
    <div className={`${cx.card} space-y-2`}>
      <div className="flex items-center justify-between gap-2">
        <h3 className={`text-sm font-bold ${cx.strong}`}>
          {SYM[a.asset]} <span className={`font-normal ${cx.muted}`}>consensus in {a.numeraire}</span>
        </h3>
        <span className={`text-[11px] ${cx.muted}`}>{a.state.replace('_', ' ')}</span>
      </div>
      {warmingUp ? (
        <ColdState>Waiting for first sample (≤ 5 min)</ColdState>
      ) : (
        <>
          <div className={`font-mono text-xl font-semibold ${cx.strong}`}>{fmtPrice(a.consensus, a.numeraire)}</div>
          <div className="flex flex-wrap gap-1.5">
            <SignalChip state={sigState(matrix, a.asset, 'S1')} prefix="S1" value={fmtBps(a.dev_bps)} title="Deviation from par (S1 peg)" />
            <SignalChip state={sigState(matrix, a.asset, 'S2')} prefix="S2" value={fmtBps(a.disp_bps, 1, false)} title="Dispersion: max − min of included mids (S2)" />
          </div>
          {a.consensus === null && a.single_venue_price !== null && (
            <p className={`text-[11px] ${cx.muted}`}>
              Single venue only ({fmtPrice(a.single_venue_price, a.numeraire)}): no consensus, so S1 is unknown.
            </p>
          )}
          <KV k="Inputs used">
            {a.inputs_used} from {a.venues_used} venue{a.venues_used === 1 ? '' : 's'}
          </KV>
          <KV k="Venues">{a.venues.length ? a.venues.join(', ') : '—'}</KV>
          <KV k="Sample">{fmtTimeHM(a.as_of)}</KV>
        </>
      )}
    </div>
  );
}

function devOption(theme: Theme, assets: StablePegAsset[], range: string) {
  const c = chrome(theme);
  const ax = axisStyle(theme);
  const daily = assets.some(a => a.series_resolution === '1d');
  const base = baseOption(
    theme,
    `Consensus deviation from par in basis points for ${assets.map(a => SYM[a.asset]).join(', ')} over ${range}, with the ±30 bps watch band and ±100 bps warning lines.`,
  );
  return {
    ...base,
    tooltip: {
      ...base.tooltip,
      trigger: 'axis',
      formatter: (params: { seriesName?: string; data?: PricePoint; color?: unknown }[]) => {
        const first = params[0]?.data?.value[0] ?? '';
        const rows: TipRow[] = params.map(p => ({
          label: p.seriesName ?? '',
          value: `${fmtBps(p.data?.value[1] ?? null)} · ${fmtPrice(p.data?.price ?? null, p.data?.numeraire ?? '')}`,
          color: colorOf(p.color),
        }));
        return tipHtml(daily ? `${first} (daily close)` : fmtDateTime(first), rows);
      },
    },
    grid: { left: 56, right: 24, top: 32, bottom: 32 },
    xAxis: { type: 'time', ...ax },
    // The axis always spans at least ±40 bps so the ±30 band reads as a band.
    yAxis: {
      type: 'value',
      name: 'bps',
      nameTextStyle: { color: c.muted },
      min: (v: { min: number }) => Math.min(Math.floor(v.min), -40),
      max: (v: { max: number }) => Math.max(Math.ceil(v.max), 40),
      ...ax,
    },
    series: assets.map((a, i) => ({
      name: SYM[a.asset],
      type: 'line',
      showSymbol: false,
      connectNulls: false,
      color: ASSET_COLOR[a.asset],
      data: withGaps(
        a.series.map(p => ({ value: [p.ts, p.dev_bps] as [string, number | null], price: p.consensus, numeraire: a.numeraire })),
        p => p.value[0],
        daily ? 2 * 86_400_000 : 15 * 60_000,
        at => ({ value: [at, null] as [string, number | null], price: null, numeraire: a.numeraire }),
      ),
      ...(i === 0
        ? {
            markArea: { silent: true, itemStyle: { color: c.band30 }, data: [[{ yAxis: -30 }, { yAxis: 30 }]] },
            markLine: {
              silent: true,
              symbol: 'none',
              lineStyle: { color: c.line100, type: 'dashed' },
              label: { color: c.muted, formatter: '{c} bps' },
              data: [{ yAxis: 100 }, { yAxis: -100 }],
            },
          }
        : {}),
    })),
  };
}

function heatOption(theme: Theme, hm: StablePegHeatmap, label: string) {
  const c = chrome(theme);
  const ax = axisStyle(theme);
  const ys = hm.rows.map(r => `${r.venue} · ${r.pair}`);
  const xs = hm.buckets.map(b => (hm.resolution === '5m' ? b.slice(11, 16) : `${b.slice(5, 10)} ${b.slice(11, 13)}h`));
  const data: [number, number, number, number][] = [];
  hm.rows.forEach((r, y) =>
    r.dev_bps.forEach((v, x) => {
      if (v !== null) data.push([x, y, v, Math.abs(v)]);
    }),
  );
  const base = baseOption(
    theme,
    `Per-venue deviation heatmap for ${label}: ${ys.length} inputs by ${xs.length} buckets of ${hm.resolution}; color shows the absolute deviation in bps, blank cells had no included sample.`,
  );
  return {
    ...base,
    tooltip: {
      ...base.tooltip,
      formatter: (p: { data?: [number, number, number, number] }) => {
        const d = p.data;
        if (!d) return '';
        return tipHtml(ys[d[1]] ?? '', [{ label: fmtDateTime(hm.buckets[d[0]]), value: fmtBps(d[2]) }], `worst included sample in the ${hm.resolution} bucket`);
      },
    },
    grid: { left: 8, right: 16, top: 8, bottom: 58, containLabel: true },
    xAxis: { type: 'category', data: xs, ...ax, splitLine: { show: false } },
    yAxis: { type: 'category', data: ys, ...ax, splitLine: { show: false }, axisLabel: { ...ax.axisLabel, width: 230, overflow: 'truncate' } },
    visualMap: {
      type: 'piecewise',
      dimension: 3,
      orient: 'horizontal',
      left: 'center',
      bottom: 0,
      itemWidth: 14,
      itemHeight: 10,
      textStyle: { color: c.muted, fontSize: 10 },
      pieces: HEAT_BINS.map((b, i) => (b.max === undefined ? { min: b.min, label: b.label, color: c.heat[i] } : { min: b.min, max: b.max, label: b.label, color: c.heat[i] })),
    },
    series: [{ type: 'heatmap', data, emphasis: { itemStyle: { borderColor: c.text, borderWidth: 1 } } }],
  };
}

function Heatmap({ hm, label }: { hm: StablePegHeatmap | null; label: string }) {
  const theme = useStableTheme();
  if (!hm) return <ColdState>The heatmap covers up to 30 days of 5-minute inputs: choose 24H, 7D or 30D.</ColdState>;
  if (hm.rows.length === 0) return <ColdState>Waiting for first sample (≤ 5 min); columns fill in as samples arrive.</ColdState>;
  return <ReactECharts option={heatOption(theme, hm, label)} notMerge style={{ height: 70 + hm.rows.length * 28 }} />;
}

function relOption(theme: Theme, peg: StablePeg) {
  const ax = axisStyle(theme);
  const rel = peg.relative;
  const daily = rel.series_resolution === '1d';
  const base = baseOption(theme, 'USDC priced in USDT: median of the venues (including Uniswap) and the Uniswap v3 pool, in basis points from parity.');
  return {
    ...base,
    tooltip: {
      ...base.tooltip,
      trigger: 'axis',
      formatter: (params: { seriesName?: string; value?: [string, number | null]; color?: unknown }[]) =>
        tipHtml(
          daily ? String(params[0]?.value?.[0] ?? '') : fmtDateTime(params[0]?.value?.[0]),
          params.map(p => ({ label: p.seriesName ?? '', value: fmtBps(p.value?.[1] ?? null), color: colorOf(p.color) })),
        ),
    },
    grid: { left: 56, right: 24, top: 32, bottom: 32 },
    xAxis: { type: 'time', ...ax },
    yAxis: { type: 'value', name: 'bps', ...ax },
    series: [
      {
        name: 'Median of venues',
        type: 'line',
        showSymbol: false,
        color: ASSET_COLOR.usdc_usdt,
        data: withGaps(rel.series.map(p => [p.ts, p.dev_bps] as [string, number | null]), p => p[0], daily ? 2 * 86_400_000 : 15 * 60_000, at => [at, null] as [string, number | null]),
      },
      {
        name: 'Uniswap v3',
        type: 'line',
        showSymbol: false,
        color: ASSET_COLOR.uniswap,
        data: withGaps(rel.uniswap_series.map(p => [p.ts, p.dev_bps] as [string, number | null]), p => p[0], daily ? 2 * 86_400_000 : 30 * 60_000, at => [at, null] as [string, number | null]),
      },
    ],
  };
}

function RelativePeg({ peg }: { peg: StablePeg }) {
  const theme = useStableTheme();
  const rel = peg.relative;
  const hasData = rel.series.length > 0 || rel.uniswap_series.length > 0;
  return (
      <Panel
        title="Relative peg: USDC in USDT"
        help="USDC priced in USDT on OKX, Binance, Coinbase (1 ÷ USDT-USDC) and the Uniswap v3 0.01% pool. The median uses every included input; the exchange median leaves Uniswap out. Deviation = (price − 1) × 10,000."
        right={
          <CsvButton
            filename={`stablecoin-relative-${peg.range}.csv`}
            label="relative peg series"
            headers={['ts', 'series', 'price', 'dev_bps']}
            rows={() => [
              ...rel.series.map(p => [p.ts, 'median', p.consensus, p.dev_bps]),
              ...rel.uniswap_series.map(p => [p.ts, 'uniswap', p.price, p.dev_bps]),
            ]}
          />
        }
      >
        <div className="mb-3 grid grid-cols-2 gap-2 md:grid-cols-4">
          <Stat label="Median" value={fmtPrice(rel.median, 'USDT')} sub={fmtBps(rel.dev_bps)} />
          <Stat label="Exchange median" value={fmtPrice(rel.exchange_median, 'USDT')} />
          <Stat label="Uniswap v3" value={fmtPrice(rel.uniswap, 'USDT')} sub={rel.uniswap === null ? undefined : fmtBps((rel.uniswap - 1) * 1e4)} />
          <Stat label="Inputs used" value={String(rel.inputs_used)} sub={`sample ${fmtTimeHM(rel.as_of)}`} />
        </div>
        {hasData ? <ReactECharts option={relOption(theme, peg)} notMerge style={{ height: 260 }} /> : <ColdState>Waiting for first sample (≤ 5 min).</ColdState>}
      </Panel>
  );
}

function DexPanel({ peg }: { peg: StablePeg }) {
  const rel = peg.relative;
  const last = rel.uniswap_series[rel.uniswap_series.length - 1];
  return (
    <Panel
      title="DEX: Uniswap v3"
      help="The USDC/USDT 0.01% pool on Ethereum, read over RPC (slot0) every 5 minutes and stored every 10 minutes. The Curve 3pool panel and the Chainlink oracle panel are v1.1."
    >
      <div className="grid grid-cols-2 gap-2">
        <Stat label="Pool price (USDC in USDT)" value={fmtPrice(rel.uniswap, 'USDT')} sub={rel.uniswap === null ? 'not read yet' : fmtBps((rel.uniswap - 1) * 1e4)} />
        <Stat label="Last stored reading" value={last ? fmtTimeHM(last.ts) : '—'} sub={`${rel.uniswap_series.length} readings in range`} />
      </div>
      <p className={`mt-3 text-[11px] ${cx.muted}`}>
        <ExtLink href={UNISWAP_POOL}>Pool 0x3416…27c6 on Uniswap</ExtLink> · S3 uses this pool’s deviation (Curve’s half of S3 is v1.1).
      </p>
    </Panel>
  );
}

function EurcMode({ a, peg, providers }: { a: StablePegAsset; peg: StablePeg; providers: StableProviderHealth[] }) {
  const fx = peg.fx;
  const usd = a.consensus !== null && fx?.eur_usd ? a.consensus * fx.eur_usd : null;
  return (
    <div className="space-y-4">
      <Panel
        title="EURC in euro terms"
        help="EURC's peg is measured in euros (§5.3). Direct EUR quotes count in full; Kraken EURCUSD ÷ ZEURZUSD is derived on one venue; Coinbase EURC-USDC × the USDC consensus ÷ EUR/USD is a cross-venue input. Inputs sharing the FX venues are flagged as partly dependent, and without a second exchange quoting EURC in euros S1 stops at warning."
      >
        <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
          <div>
            <KV k="EUR consensus">
              <span className="font-mono">{fmtPrice(a.consensus, 'EUR')}</span>
            </KV>
            <KV k="Deviation">
              <span className="font-mono">{fmtBps(a.dev_bps)}</span>
            </KV>
            <KV k="Direct EUR venues">{a.direct_eur_venues ?? '—'}</KV>
            <KV k="Second direct EUR venue">{fmtEurcBool(a.second_direct_eur_venue, 'S1 is capped at warning')}</KV>
            <KV k="Partly dependent inputs">{fmtEurcBool(a.partially_dependent, undefined, 'derived inputs share the FX venues')}</KV>
          </div>
          <div>
            <KV k="EUR/USD consensus">
              <span className="font-mono">{fx?.eur_usd == null ? '—' : fx.eur_usd.toFixed(5)}</span>
              {fx && ` (${fx.venues_used} venue${fx.venues_used === 1 ? '' : 's'}${peg.fx_single_venue ? ', single venue' : ''})`}
            </KV>
            <KV k="ECB reference rate">
              <span className="font-mono">{peg.ecb ? peg.ecb.rate.toFixed(4) : '—'}</span>
              {peg.ecb && ` (${peg.ecb.day})`}
            </KV>
            <KV k="EURC in USD">
              <span className="font-mono">{fmtPrice(usd, 'USD')}</span>
            </KV>
            <KV k="Volume conversion">{fx?.volume_source ? `${fx.volume_source} rate` : '—'}</KV>
          </div>
        </div>
        <p className={`mt-3 text-[11px] ${cx.muted}`}>
          EURC is a euro instrument: its USD value moves with EUR/USD, so a falling dollar price is not a depeg. The dashboard measures it against €1 and shows USD only for
          reference.
        </p>
      </Panel>
      <VenueTable
        title="EUR/USD inputs"
        quotes={fx?.quotes ?? []}
        numeraire=""
        providers={providers}
        filename="stablecoin-eurusd-inputs.csv"
        help="EUR/USD mids from Kraken and Bitstamp; their median converts USD-quoted EURC inputs to euros. The ECB rate is the volume-filter fallback and the reference for EURC's daily history."
      />
    </div>
  );
}

function depegOption(theme: Theme, assets: StablePegAsset[]) {
  const c = chrome(theme);
  const ax = axisStyle(theme);
  const base = baseOption(
    theme,
    `Daily close deviation in bps since ${assets.map(a => a.history_from).filter(Boolean).sort()[0] ?? 'the first stored day'} for ${assets.map(a => SYM[a.asset]).join(', ')}, with Bitstamp intraday lows as markers and curated depeg annotations.`,
  );
  const ann: StableAnnotation[] = assets.flatMap(a => a.annotations);
  const lines = assets.map((a, i) => {
    const lineCloses = a.asset === 'eurc' ? a.daily_closes.filter(d => d.source !== 'bitstamp_ohlc') : a.daily_closes;
    const markData =
      i === 0
        ? [
            { yAxis: 30, name: '+30', lineStyle: { color: c.muted, type: 'dotted' } },
            { yAxis: -30, name: '−30', lineStyle: { color: c.muted, type: 'dotted' } },
            { yAxis: 100, name: '+100', lineStyle: { color: c.line100, type: 'dashed' } },
            { yAxis: -100, name: '−100', lineStyle: { color: c.line100, type: 'dashed' } },
            ...ann.map(x => ({ xAxis: x.date, name: `${x.asset}: ${x.title}`, lineStyle: { color: c.muted, type: 'solid', opacity: 0.6 } })),
          ]
        : [];
    return {
      name: `${SYM[a.asset]} daily close`,
      type: 'line',
      showSymbol: false,
      color: ASSET_COLOR[a.asset],
      data: lineCloses.map(d => ({ value: [d.day, d.dev_bps], close: d.close, source: d.source, numeraire: a.numeraire })),
      ...(markData.length > 0
        ? {
            markLine: {
              silent: true,
              symbol: 'none',
              label: { color: c.muted, fontSize: 10, formatter: (p: { name?: string }) => p.name ?? '' },
              data: markData,
            },
          }
        : {}),
    };
  });
  const eurcBitstampPts = assets
    .filter(a => a.asset === 'eurc')
    .flatMap(a => {
      const pts = a.daily_closes.filter(d => d.source === 'bitstamp_ohlc');
      if (pts.length === 0) return [];
      return [
        {
          name: 'EURC Bitstamp close (≥ 200k vol)',
          type: 'scatter',
          symbol: 'circle',
          symbolSize: 6,
          color: ASSET_COLOR.eurc,
          data: pts.map(d => ({ value: [d.day, d.dev_bps], close: d.close, source: 'Bitstamp close (≥ 200k vol)', numeraire: a.numeraire })),
        },
      ];
    });
  const marks = assets
    .filter(a => a.low_markers.length > 0)
    .map(a => ({
      name: `${SYM[a.asset]} Bitstamp low`,
      type: 'scatter',
      symbol: 'triangle',
      symbolRotate: 180,
      symbolSize: 9,
      color: ASSET_COLOR[a.asset],
      data: a.low_markers.map(m => ({ value: [m.day, m.low_dev_bps], close: m.low, source: `Bitstamp low; confirmed by ${m.confirmed_by.join(', ')}`, numeraire: a.numeraire })),
    }));
  return {
    ...base,
    tooltip: {
      ...base.tooltip,
      trigger: 'axis',
      formatter: (params: { seriesName?: string; data?: { value: [string, number]; close: number; source: string; numeraire: string }; color?: unknown }[]) => {
        const day = params[0]?.data?.value[0] ?? '';
        const notes = ann.filter(x => x.date === day).map(x => `${x.asset}: ${x.title}${x.extreme ? ` (${x.extreme})` : ''}`);
        return tipHtml(
          day,
          params.map(p => ({
            label: p.seriesName ?? '',
            value: `${fmtBps(p.data?.value[1] ?? null)} · ${fmtPrice(p.data?.close ?? null, p.data?.numeraire ?? '')} · ${p.data?.source ?? ''}`,
            color: colorOf(p.color),
          })),
          notes.length ? notes.join(' · ') : undefined,
        );
      },
    },
    legend: { ...base.legend, type: 'scroll' },
    grid: { left: 64, right: 24, top: 32, bottom: 64 },
    xAxis: { type: 'time', ...ax },
    yAxis: { type: 'value', name: 'bps', ...ax },
    dataZoom: [{ type: 'inside' }, { type: 'slider', height: 16, bottom: 12, textStyle: { color: c.muted } }],
    series: [...lines, ...eurcBitstampPts, ...marks],
  };
}

function DepegHistory({ peg }: { peg: StablePeg }) {
  const theme = useStableTheme();
  const assets = peg.assets.filter(a => a.daily_closes.length > 0 || a.low_markers.length > 0);
  const hasEurc = peg.assets.some(a => a.asset === 'eurc');
  return (
    <div className="space-y-4">
      <Panel
        title="Depeg history"
        help="A day counts by its daily rule at ±30 bps: the BigLens consensus rollup close, else for EURC Kraken EURCEUR same-sign min(|close_dev|, |vwap_dev|) from 2025-09-18 onward and Bitstamp EURC/EUR close on days with ≥ 200k EURC volume before 2025-09-18 (other pre-Kraken days are not classified), else DefiLlama's daily price for USD coins. A Bitstamp intraday low is a marker only when it is at least 100 bps off and that day's candle close, BigLens close or DefiLlama price is at least 30 bps off."
        right={
          <CsvButton
            filename="stablecoin-depeg-history.csv"
            label="daily closes"
            headers={['day', 'asset', 'source', 'close', 'vwap', 'close_dev_bps', 'vwap_dev_bps', 'dev_bps', 'threshold_bps']}
            rows={() =>
              peg.assets.flatMap(a =>
                a.daily_closes.map(d => [
                  d.day,
                  a.asset,
                  d.source,
                  d.close,
                  d.vwap ?? null,
                  d.close_dev_bps ?? null,
                  d.vwap_dev_bps ?? null,
                  d.dev_bps,
                  d.threshold_bps,
                ]),
              )
            }
          />
        }
      >
        <div className="mb-3 grid grid-cols-1 gap-2 md:grid-cols-3">
          {peg.assets.map(a => {
            const l = a.last_deviation;
            const srcLabel = l?.source_label || l?.source || '';
            const label = l
              ? `${SYM[a.asset]}: last ≥${l.threshold_bps} bps (${srcLabel})`
              : a.numeraire === 'EUR'
                ? `${SYM[a.asset]}: last ≥30 bps (Kraken close & VWAP / Bitstamp close ≥ 200k vol)`
                : `${SYM[a.asset]}: last ≥100 bps deviation`;
            return (
              <Stat
                key={a.asset}
                label={label}
                value={l ? `${l.days_since} days ago` : '—'}
                sub={l ? `${l.day} · ${fmtBps(l.dev_bps, 0)} · ${srcLabel}` : a.history_from ? `none since ${a.history_from}` : 'loading history'}
              />
            );
          })}
        </div>
        {assets.length === 0 ? (
          <ColdState>Loading history: the daily DefiLlama and Bitstamp bootstrap runs at boot.</ColdState>
        ) : (
          <>
            <ReactECharts option={depegOption(theme, assets)} notMerge style={{ height: 360 }} />
            {hasEurc && (
              <p className={`mt-2 text-[11px] ${cx.muted}`}>
                2022-06 to 2025-09: Bitstamp EURC/EUR close on days with ≥ 200k EURC volume; other days not classified. From 2025-09-18 onward (Kraken EURCEUR daily OHLC), the
                day deviation is the same-sign smaller magnitude of close and VWAP deviations (0 when signs differ) against ±30 bps.
              </p>
            )}
          </>
        )}
      </Panel>
      <Panel
        title="Depeg days"
        help="Stored days whose classified deviation is at or beyond the threshold (inclusive), newest first; Kraken EURCEUR rows show close deviation, VWAP deviation, and the classified deviation; consensus days also show the intraday range and minutes at ≥ 30 and ≥ 100 bps."
        right={
          <CsvButton
            filename="stablecoin-depeg-days.csv"
            label="depeg days"
            headers={[
              'day',
              'asset',
              'source',
              'close',
              'vwap',
              'close_dev_bps',
              'vwap_dev_bps',
              'dev_bps',
              'threshold_bps',
              'min_dev_bps',
              'max_dev_bps',
              'minutes_ge_30',
              'minutes_ge_100',
            ]}
            rows={() =>
              peg.assets.flatMap(a =>
                a.depeg_days.map(d => [
                  d.day,
                  a.asset,
                  d.source,
                  d.close,
                  d.vwap ?? null,
                  d.close_dev_bps,
                  d.vwap_dev_bps ?? null,
                  d.dev_bps,
                  d.threshold_bps,
                  d.min_dev_bps ?? null,
                  d.max_dev_bps ?? null,
                  d.minutes_ge_30 ?? null,
                  d.minutes_ge_100 ?? null,
                ]),
              )
            }
          />
        }
      >
        {peg.assets.every(a => a.depeg_days.length === 0) ? (
          <ColdState>No stored day closes off peg.</ColdState>
        ) : (
          <div className="max-h-[320px] overflow-auto">
            <table className={cx.table}>
              <thead>
                <tr>
                  {['Day', 'Asset', 'Source', 'Close', 'Close deviation', 'VWAP deviation', 'Classified deviation', 'Threshold', 'Intraday range', 'Minutes ≥ 30 / ≥ 100'].map(h => (
                    <th key={h} scope="col" className={cx.th}>
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {peg.assets.flatMap(a =>
                  a.depeg_days.map(d => (
                    <tr key={`${a.asset}-${d.day}`} className={cx.tr}>
                      <td className={`${cx.td} font-mono`}>{d.day}</td>
                      <td className={cx.td}>{SYM[a.asset]}</td>
                      <td className={cx.td}>{d.source_label || d.source}</td>
                      <td className={`${cx.td} font-mono`}>{fmtPrice(d.close, a.numeraire)}</td>
                      <td className={`${cx.td} font-mono`}>{fmtBps(d.close_dev_bps)}</td>
                      <td className={`${cx.td} font-mono`}>{fmtBps(d.vwap_dev_bps ?? null)}</td>
                      <td className={`${cx.td} font-mono`}>{fmtBps(d.dev_bps)}</td>
                      <td className={`${cx.td} font-mono`}>{d.threshold_bps} bps</td>
                      <td className={`${cx.td} font-mono`}>
                        {d.min_dev_bps == null ? '—' : `${fmtBps(d.min_dev_bps)} … ${fmtBps(d.max_dev_bps ?? null)}`}
                      </td>
                      <td className={`${cx.td} font-mono`}>{d.minutes_ge_30 == null ? '—' : `${d.minutes_ge_30} / ${d.minutes_ge_100 ?? 0}`}</td>
                    </tr>
                  )),
                )}
              </tbody>
            </table>
          </div>
        )}
        {peg.assets.some(a => a.depeg_days_total > a.depeg_days.length) && (
          <p className={`mt-2 text-[11px] ${cx.faint}`}>The list shows the newest 500 days per asset; the CSV has the same rows.</p>
        )}
      </Panel>
      <Panel
        title="Historical depeg annotations"
        help="Curated events with sources (Appendix C), including coins outside the core set for context."
        right={
          <CsvButton
            filename="stablecoin-depeg-annotations.csv"
            label="depeg annotations"
            headers={['date', 'end_date', 'asset', 'title', 'extreme', 'low_price', 'status', 'source_url']}
            rows={() => peg.annotations.map(x => [x.date, x.end_date ?? '', x.asset, x.title, x.extreme ?? '', x.low_price ?? null, x.status ?? '', x.source_url ?? ''])}
          />
        }
      >
        {peg.annotations.length === 0 ? (
          <ColdState>No curated annotations.</ColdState>
        ) : (
          <div className="overflow-x-auto">
            <table className={cx.table}>
              <thead>
                <tr>
                  {['Date', 'Coin', 'Event', 'Extreme', 'Source'].map(h => (
                    <th key={h} scope="col" className={cx.th}>
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {peg.annotations.map(x => (
                  <tr key={`${x.date}-${x.asset}`} className={cx.tr}>
                    <td className={`${cx.td} font-mono`}>
                      {x.date}
                      {x.end_date ? ` → ${x.end_date}` : ''}
                    </td>
                    <td className={cx.td}>{x.asset}</td>
                    <td className={`${cx.tdWrap}`}>{x.title}</td>
                    <td className={cx.tdWrap}>{x.extreme || '—'}</td>
                    <td className={cx.td}>{x.source_url ? <ExtLink href={x.source_url}>source</ExtLink> : x.status || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {peg.annotations_overridden && <p className={`mt-2 text-[11px] ${cx.muted}`}>Annotations come from the operator's facts override.</p>}
      </Panel>
    </div>
  );
}

function PegSignals({ rows }: { rows: StableMatrixRow[] }) {
  return (
    <Panel
      title="Peg signals"
      help="S1 peg (|dev| ≥ 30 / 100 / 300 bps sustained 15 min, or ≥ 2 venues ≥ 500 bps at once), S2 dispersion (≥ 25 / 100 bps with ≥ 3 inputs), S3 DEX (Uniswap |dev| ≥ 30 / 100 bps), S13 data health."
    >
      <SignalMatrix rows={rows} only={['S1', 'S2', 'S3', 'S13']} />
    </Panel>
  );
}

export default function PegTab({
  asset,
  range,
  active,
  refreshTick,
  matrix,
  providers,
}: {
  asset: StableAsset;
  range: StableRange;
  active: boolean;
  refreshTick: number;
  matrix: StableMatrixRow[];
  providers: StableProviderHealth[];
}) {
  const theme = useStableTheme();
  const poll = usePolling(() => fetchStablePeg(asset, range), 60_000, `stable-peg-${asset}-${range}-${refreshTick}`, active);
  const peg = poll.data;
  if (!peg) {
    return poll.error ? <ErrorNote message={poll.error} /> : <ColdState>Loading the peg view…</ColdState>;
  }
  const rows = matrix.filter(r => asset === 'all' || r.asset === asset);
  const eurc = peg.assets.find(a => a.asset === 'eurc');
  const hasSeries = peg.assets.some(a => a.series.length > 0);
  return (
    <div className="space-y-5">
      {poll.error && <ErrorNote message={poll.error} />}
      {peg.stale && (
        <p className="rounded-xl border border-amber-300 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          The latest peg sample is older than three intervals (15 min); the values below are the last good ones.
        </p>
      )}
      {peg.assets.length === 1 ? (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
          <ConsensusCard a={peg.assets[0]} warmingUp={peg.warming_up} matrix={matrix} />
          <div className="lg:col-span-2">
            <PegSignals rows={rows} />
          </div>
        </div>
      ) : (
        <>
          <PegSignals rows={rows} />
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
            {peg.assets.map(a => (
              <ConsensusCard key={a.asset} a={a} warmingUp={peg.warming_up} matrix={matrix} />
            ))}
          </div>
        </>
      )}
      <Panel
        title="Consensus deviation"
        help="Deviation of the consensus from par: (consensus − 1) × 10,000 in the asset's numéraire (EURC in euros). Up to 30 days the series is the 5-minute samples, beyond that the daily close. Shaded: ±30 bps (S1 watch); dashed: ±100 bps (S1 warning)."
        right={
          <CsvButton
            filename={`stablecoin-consensus-${asset}-${peg.range}.csv`}
            label="consensus series"
            headers={['ts', 'asset', 'consensus', 'dev_bps', 'disp_bps', 'venues_used', 'state']}
            rows={() => peg.assets.flatMap(a => a.series.map(p => [p.ts, a.asset, p.consensus, p.dev_bps, p.disp_bps ?? null, p.venues_used ?? null, p.state ?? '']))}
          />
        }
      >
        {hasSeries ? (
          <ReactECharts option={devOption(theme, peg.assets, peg.range)} notMerge style={{ height: 300 }} />
        ) : (
          <ColdState>Waiting for first sample (≤ 5 min).</ColdState>
        )}
      </Panel>
      <Panel
        title="Per-venue deviation heatmap"
        help="One row per consensus input, one column per 5-minute (24H) or hourly (7D, 30D) bucket; a cell is the input's worst included deviation in the bucket. The scale is sequential in |deviation| (color-blind safe); blank cells were excluded or not sampled."
        right={
          <CsvButton
            filename={`stablecoin-heatmap-${asset}-${peg.range}.csv`}
            label="heatmap cells"
            headers={['asset', 'venue', 'pair', 'kind', 'bucket', 'dev_bps']}
            rows={() =>
              peg.assets.flatMap(a =>
                (a.heatmap?.rows ?? []).flatMap(r => r.dev_bps.map((v, i) => [a.asset, r.venue, r.pair, r.kind, a.heatmap?.buckets[i] ?? '', v])),
              )
            }
          />
        }
      >
        <div className="space-y-4">
          {peg.assets.map(a => (
            <div key={a.asset}>
              <h4 className={`mb-1 text-xs font-semibold ${cx.text}`}>{SYM[a.asset]}</h4>
              <Heatmap hm={a.heatmap} label={SYM[a.asset]} />
            </div>
          ))}
        </div>
      </Panel>
      {peg.assets.map(a => (
        <VenueTable
          key={a.asset}
          title={`${SYM[a.asset]} consensus inputs`}
          quotes={a.quotes}
          numeraire={a.numeraire}
          providers={providers}
          filename={`stablecoin-inputs-${a.asset}.csv`}
        />
      ))}
      <details className={`${cx.card} group`}>
        <summary className={`cursor-pointer text-sm font-semibold ${cx.strong} ${cx.focus}`}>All venue markets ({peg.markets.length})</summary>
        <div className="mt-3">
          <VenueTable
            title="Venue markets"
            quotes={peg.markets}
            numeraire=""
            providers={providers}
            filename="stablecoin-venue-markets.csv"
            help="Every raw market of the latest sample, evaluated on its own (no deviation: quote currencies differ)."
          />
        </div>
      </details>
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <div className="xl:col-span-2">
          <RelativePeg peg={peg} />
        </div>
        <DexPanel peg={peg} />
      </div>
      <Panel title="Relative peg heatmap" help="Each relative input's deviation per bucket (worst included sample in the bucket).">
        <Heatmap hm={peg.relative.heatmap} label="USDC in USDT" />
      </Panel>
      <VenueTable
        title="Relative peg inputs"
        quotes={peg.relative.quotes}
        numeraire="USDT"
        providers={providers}
        filename="stablecoin-relative-inputs.csv"
        help="USDC priced in USDT on each venue; Coinbase quotes USDT-USDC, so it is inverted."
      />
      {eurc && <EurcMode a={eurc} peg={peg} providers={providers} />}
      <DepegHistory peg={peg} />
    </div>
  );
}

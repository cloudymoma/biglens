import React, { useState, useEffect, useMemo } from 'react';
import ReactECharts from 'echarts-for-react';
import {
  ChevronDown, Globe2, Flame, TrendingUp, CalendarDays, Search, X, Trophy,
} from 'lucide-react';
import type { TrendsMeta, TrendsDashboardData, TrendsTermData, SemMarket } from '../types';
import { fetchTrendsMeta, fetchTrendsDashboard, fetchTrendsTerm } from '../api';
import { MetricCard, EmptyState, ErrorBanner } from '../dashboards/shared';
import PulsePanel from './sem/PulsePanel';

// Categorical palette for the compare chart, validated for the dark surface
// (lightness band, chroma, CVD separation, contrast). Colors follow the term:
// each term keeps its slot until removed, never repainted by rank.
const SERIES_COLORS = ['#0284c7', '#d97706', '#8b5cf6', '#059669', '#f43f5e'];
const MAX_COMPARE_TERMS = 5;

const CHART_TOOLTIP = {
  backgroundColor: 'rgba(17,17,20,0.95)',
  borderColor: '#27272a',
  textStyle: { color: '#e4e4e7', fontSize: 12 },
};

const AXIS_LABEL = { color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' };
const SPLIT_LINE = { lineStyle: { color: '#1f1f23', type: 'dashed' } };

// Minimal shapes of the ECharts callback params we actually read.
interface ChartDatum { name: string; value: number }
interface SeriesLabelParam { seriesName: string }

export default function GoogleTrendsDashboard() {
  const [meta, setMeta] = useState<TrendsMeta | null>(null);
  const [metaError, setMetaError] = useState('');

  // Same filter model as the SEM dashboard: US market at Nielsen DMA grain
  // ('' = national), Global market at country grain. US/international
  // partition dates are aligned, so one meta payload serves both.
  const [market, setMarket] = useState<SemMarket>('us');
  const [refreshDate, setRefreshDate] = useState('');
  const [countryCode, setCountryCode] = useState(''); // global market
  const [dma, setDma] = useState(''); // us market; '' = national

  const [data, setData] = useState<TrendsDashboardData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  // term -> assigned series color; key insertion order is the compare order.
  const [termColors, setTermColors] = useState<Record<string, string>>({});
  const [focusTerm, setFocusTerm] = useState('');
  const [termData, setTermData] = useState<TrendsTermData | null>(null);
  const [termLoading, setTermLoading] = useState(false);
  const [termError, setTermError] = useState('');

  const selectedTerms = useMemo(() => Object.keys(termColors), [termColors]);

  // The country_code/dma pair actually sent to the API for the active market.
  const geoCode = market === 'us' ? 'US' : countryCode;
  const geoDma = market === 'us' ? dma : '';

  useEffect(() => {
    fetchTrendsMeta()
      .then(m => {
        setMeta(m);
        setRefreshDate(m.latest_refresh_date);
        const codes = m.countries.map(c => c.code);
        const preferred = ['GB', 'JP'].find(c => codes.includes(c));
        setCountryCode(preferred || codes[0] || '');
      })
      .catch(e => setMetaError(e.response?.data || e.message));
  }, []);

  useEffect(() => {
    if (!refreshDate || (market === 'global' && !countryCode)) return;
    setLoading(true);
    setError('');
    fetchTrendsDashboard(refreshDate, geoCode, geoDma)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [market, refreshDate, countryCode, dma]);

  // Seed the compare set with the #1 top term on first load.
  useEffect(() => {
    if (data && selectedTerms.length === 0 && data.top_terms?.length > 0) {
      selectTerm(data.top_terms[0].term);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data]);

  useEffect(() => {
    if (!refreshDate || (!focusTerm && selectedTerms.length === 0)) return;
    if (market === 'global' && !countryCode) return;
    setTermLoading(true);
    setTermError('');
    fetchTrendsTerm(refreshDate, geoCode, focusTerm, selectedTerms, geoDma)
      .then(setTermData)
      .catch(e => setTermError(e.response?.data || e.message))
      .finally(() => setTermLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [market, refreshDate, countryCode, dma, focusTerm, selectedTerms]);

  function selectTerm(term: string) {
    setTermColors(prev => {
      if (prev[term] || Object.keys(prev).length >= MAX_COMPARE_TERMS) return prev;
      const used = new Set(Object.values(prev));
      const color = SERIES_COLORS.find(c => !used.has(c)) ?? SERIES_COLORS[0];
      return { ...prev, [term]: color };
    });
    setFocusTerm(term);
  }

  function removeTerm(term: string) {
    setTermColors(prev => {
      const next = { ...prev };
      delete next[term];
      return next;
    });
    if (focusTerm === term) {
      const remaining = selectedTerms.filter(t => t !== term);
      setFocusTerm(remaining[0] || '');
    }
  }

  if (metaError) return <ErrorBanner message={metaError} />;
  if (!meta) return <LoadingPulse />;

  const countryName = meta.countries.find(c => c.code === countryCode)?.name || countryCode;
  const geoLabel = market === 'us' ? (dma || 'the United States') : countryName;
  const topTerms = data?.top_terms || [];
  const risingTerms = data?.rising_terms || [];
  const hottest = risingTerms[0];

  return (
    <div className="space-y-6">
      {/* Filter bar: market toggle + geo + refresh date + term search */}
      <div className="rounded-2xl border border-zinc-800/50 p-4 flex flex-wrap items-end gap-4" style={{ background: '#111114' }}>
        <div>
          <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">Market</label>
          <div className="flex rounded-lg border border-zinc-800/50 overflow-hidden" style={{ background: '#09090b' }}>
            {(['us', 'global'] as SemMarket[]).map(m => (
              <button
                key={m}
                onClick={() => setMarket(m)}
                className={`px-3 py-2 text-xs cursor-pointer transition-colors ${
                  market === m ? 'text-cyan-400 bg-cyan-500/10' : 'text-zinc-500 hover:text-zinc-300'
                }`}
              >
                {m === 'us' ? 'US Metro (DMA)' : 'Global'}
              </button>
            ))}
          </div>
        </div>
        <CountryCombobox
          label={market === 'us' ? 'Metro Area (DMA)' : 'Country'}
          countries={market === 'us'
            ? [{ name: 'All US (national)', code: '' }, ...meta.dmas.map(d => ({ name: d.name, code: d.name }))]
            : meta.countries}
          value={market === 'us' ? dma : countryCode}
          onChange={code => (market === 'us' ? setDma(code) : setCountryCode(code))}
        />
        <div>
          <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">Refresh Date</label>
          <div className="relative">
            <select
              value={refreshDate}
              onChange={e => setRefreshDate(e.target.value)}
              className="text-xs text-zinc-400 rounded-lg pl-3 pr-8 py-2 outline-none cursor-pointer appearance-none border border-zinc-800/50 transition-colors focus:border-cyan-500/30"
              style={{ background: '#09090b', minWidth: 140 }}
            >
              {meta.refresh_dates.map(d => <option key={d} value={d}>{d}</option>)}
            </select>
            <ChevronDown size={12} className="absolute right-2.5 top-1/2 -translate-y-1/2 pointer-events-none text-zinc-600" />
          </div>
        </div>
        <TermSearchBox
          topTerms={topTerms.map(t => t.term)}
          risingTerms={risingTerms.map(t => t.term)}
          onSelect={selectTerm}
        />
        <p className="text-[10px] text-zinc-600 ml-auto self-center max-w-[220px] leading-relaxed">
          Click any term to map it across countries and add it to the 5-year comparison (up to {MAX_COMPARE_TERMS}).
        </p>
      </div>

      {error && <ErrorBanner message={error} />}
      {loading && <LoadingPulse />}

      {!loading && !error && data && (
        <>
          {/* Summary metrics */}
          <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
            <MetricCard label="Top Term" value={topTerms[0]?.term || '—'} icon={<Trophy size={18} />}
              detail={`Rank #1 in ${geoLabel}`} accentColor="#38bdf8" />
            <MetricCard label="Hottest Riser" value={hottest ? `+${hottest.percent_gain.toLocaleString()}%` : '—'}
              icon={<Flame size={18} />} detail={hottest?.term || 'No rising terms'} accentColor="#fbbf24" />
            <MetricCard label={market === 'us' ? 'Metros Tracked' : 'Countries Tracked'}
              value={String(market === 'us' ? meta.dmas.length : meta.countries.length)} icon={<Globe2 size={18} />}
              detail="In this snapshot" accentColor="#34d399" />
            <MetricCard label="Snapshot" value={refreshDate} icon={<CalendarDays size={18} />}
              detail="Daily refresh, 5y weekly history" accentColor="#a78bfa" />
          </div>

          {/* Module 1: Top terms leaderboard + term cloud */}
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            <div className="rounded-2xl border border-zinc-800/50 p-6 flex flex-col" style={{ background: '#111114' }}>
              <h3 className="text-sm font-semibold text-white mb-1">Top Terms Leaderboard</h3>
              <p className="text-xs text-zinc-500 mb-4">Top 25 search terms in {geoLabel}</p>
              {topTerms.length > 0 ? (
                // The absolutely-positioned scroller contributes no intrinsic
                // height, so the card tracks its grid sibling (the term cloud)
                // and the list fills whatever height that yields.
                <div className="relative flex-1 min-h-[300px]">
                  <div className="absolute inset-0 overflow-y-auto pr-2 space-y-1">
                  {topTerms.map(t => (
                    <button
                      key={t.term}
                      onClick={() => selectTerm(t.term)}
                      className={`w-full flex items-center gap-3 px-3 py-2 rounded-lg text-left cursor-pointer transition-colors border ${
                        focusTerm === t.term
                          ? 'border-cyan-500/30 bg-cyan-500/5'
                          : 'border-transparent hover:bg-zinc-800/40'
                      }`}
                    >
                      <span className="w-6 text-[11px] font-mono text-zinc-600 text-right shrink-0">{t.rank}</span>
                      <span className="flex-1 text-xs text-zinc-300 truncate">{t.term}</span>
                      <span className="w-24 shrink-0 h-1.5 rounded-full bg-zinc-800/80 overflow-hidden">
                        <span className="block h-full rounded-full" style={{ width: `${t.score}%`, background: 'linear-gradient(90deg, #164e63, #0284c7)' }} />
                      </span>
                      <span className="w-8 text-[11px] font-mono text-zinc-500 text-right shrink-0">{t.score}</span>
                    </button>
                  ))}
                  </div>
                </div>
              ) : (
                <EmptyState text="No top terms for this selection" />
              )}
            </div>

            <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
              <h3 className="text-sm font-semibold text-white mb-1">Term Cloud</h3>
              <p className="text-xs text-zinc-500 mb-4">Size follows score; top 5 ranks highlighted</p>
              {topTerms.length > 0 ? (
                <div className="flex flex-wrap items-baseline justify-center gap-x-4 gap-y-2 py-6 px-2 min-h-[300px] content-center">
                  {topTerms.map(t => (
                    <button
                      key={t.term}
                      onClick={() => selectTerm(t.term)}
                      title={`Rank #${t.rank} · score ${t.score}`}
                      className={`cursor-pointer transition-colors leading-tight ${
                        t.rank <= 5 ? 'text-cyan-300 hover:text-cyan-200'
                          : t.rank <= 15 ? 'text-zinc-400 hover:text-zinc-200'
                            : 'text-zinc-600 hover:text-zinc-400'
                      } ${focusTerm === t.term ? 'underline underline-offset-4' : ''}`}
                      style={{ fontSize: `${11 + t.score * 0.22}px`, fontWeight: t.rank <= 5 ? 600 : 400 }}
                    >
                      {t.term}
                    </button>
                  ))}
                </div>
              ) : (
                <EmptyState text="No terms to display" />
              )}
            </div>
          </div>

          {/* Module 2: Rising terms */}
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
              <h3 className="text-sm font-semibold text-white mb-1">Surging Terms</h3>
              <p className="text-xs text-zinc-500 mb-4">Top 10 by week-over-week growth</p>
              {risingTerms.length > 0 ? (
                <div className="h-[340px]">
                  <ReactECharts
                    option={risingBarOption(risingTerms.slice(0, 10))}
                    style={{ height: '100%' }}
                    onEvents={{ click: (p: ChartDatum) => selectTerm(p.name) }}
                  />
                </div>
              ) : (
                <EmptyState text="No rising terms for this selection" />
              )}
            </div>

            <div className="rounded-2xl border border-zinc-800/50 p-6 flex flex-col" style={{ background: '#111114' }}>
              <h3 className="text-sm font-semibold text-white mb-1">Rising Terms Breakdown</h3>
              <p className="text-xs text-zinc-500 mb-4">Growth and current score in {geoLabel}</p>
              {risingTerms.length > 0 ? (
                <div className="relative flex-1 min-h-[300px]">
                  <div className="absolute inset-0 overflow-y-auto pr-2 space-y-1">
                  {risingTerms.map(t => (
                    <button
                      key={t.term}
                      onClick={() => selectTerm(t.term)}
                      className={`w-full flex items-center gap-3 px-3 py-2 rounded-lg text-left cursor-pointer transition-colors border ${
                        focusTerm === t.term
                          ? 'border-amber-500/30 bg-amber-500/5'
                          : 'border-transparent hover:bg-zinc-800/40'
                      }`}
                    >
                      <span className="w-6 text-[11px] font-mono text-zinc-600 text-right shrink-0">{t.rank}</span>
                      <span className="flex-1 text-xs text-zinc-300 truncate">{t.term}</span>
                      <span className="text-[10px] font-semibold font-mono px-2 py-0.5 rounded-md border text-amber-400 border-amber-500/20 bg-amber-500/5 shrink-0">
                        +{t.percent_gain.toLocaleString()}%
                      </span>
                      <span className="w-8 text-[11px] font-mono text-zinc-500 text-right shrink-0">{t.score}</span>
                    </button>
                  ))}
                  </div>
                </div>
              ) : (
                <EmptyState text="No rising terms for this selection" />
              )}
            </div>
          </div>
        </>
      )}

      {/* Module 3: geo spread for the focused term — cross-country for the
          international tables, Nielsen DMA breakdown in the US view (the US
          never appears in the international tables). */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">
          {market === 'us' ? 'US Metro Interest' : 'Cross-Country Interest'}
          {focusTerm ? <span className="text-cyan-400"> · {focusTerm}</span> : ''}
        </h3>
        <p className="text-xs text-zinc-500 mb-4">
          {market === 'us'
            ? 'Score across the Nielsen DMAs where the term charts or rises'
            : 'Latest score wherever the term charts in the top 25'}
        </p>
        {termError && <ErrorBanner message={termError} />}
        {!termError && (termLoading ? (
          <div className="h-[300px] rounded-xl animate-pulse" style={{ background: '#09090b' }} />
        ) : (termData?.geo?.length || 0) > 0 ? (
          <div className="h-[300px]">
            <ReactECharts option={geoBarOption(termData!.geo.slice(0, 20), countryCode)} style={{ height: '100%' }} />
          </div>
        ) : (
          <EmptyState text={focusTerm
            ? `"${focusTerm}" is not ${market === 'us' ? 'charting in any US metro' : "in any country's top 25"} this week`
            : 'Select a term above'} />
        ))}
      </div>

      {/* Module 4: 5-year weekly history comparison */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <div className="flex items-center justify-between flex-wrap gap-2 mb-1">
          <h3 className="text-sm font-semibold text-white">Interest Over Time</h3>
          {selectedTerms.length >= MAX_COMPARE_TERMS && (
            <span className="text-[10px] text-zinc-600">Compare list full — remove a term to add another</span>
          )}
        </div>
        <p className="text-xs text-zinc-500 mb-3">Weekly score over 5 years in {geoLabel} · drag to zoom</p>

        {selectedTerms.length > 0 && (
          <div className="flex flex-wrap gap-2 mb-4">
            {selectedTerms.map(term => (
              <span
                key={term}
                className={`flex items-center gap-1.5 pl-2.5 pr-1.5 py-1 rounded-lg border text-xs cursor-pointer transition-colors ${
                  focusTerm === term ? 'border-zinc-600 text-zinc-200' : 'border-zinc-800/60 text-zinc-400 hover:text-zinc-200'
                }`}
                style={{ background: '#09090b' }}
                onClick={() => setFocusTerm(term)}
              >
                <span className="h-2 w-2 rounded-full shrink-0" style={{ background: termColors[term] }} />
                {term}
                <button
                  onClick={e => { e.stopPropagation(); removeTerm(term); }}
                  className="p-0.5 rounded text-zinc-600 hover:text-zinc-300 cursor-pointer"
                  title={`Remove ${term}`}
                >
                  <X size={11} />
                </button>
              </span>
            ))}
          </div>
        )}

        {termLoading ? (
          <div className="h-[360px] rounded-xl animate-pulse" style={{ background: '#09090b' }} />
        ) : (termData?.history?.length || 0) > 0 ? (
          <div className="h-[360px]">
            <ReactECharts option={historyOption(termData!.history, termColors)} style={{ height: '100%' }} notMerge />
          </div>
        ) : (
          <EmptyState text="Click terms in the tables above to chart their 5-year history" />
        )}
      </div>

      {/* Module 5: hourly national pulse (US only — the hourly public table
          has no international counterpart). Reuses the SEM pulse endpoint. */}
      {market === 'us' && <PulsePanel onSelectTerm={selectTerm} />}
    </div>
  );
}

// --- Chart options ---

function risingBarOption(rising: { term: string; percent_gain: number }[]) {
  const sorted = [...rising].sort((a, b) => a.percent_gain - b.percent_gain);
  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      ...CHART_TOOLTIP,
      formatter: (params: ChartDatum[]) => {
        const p = params[0];
        return `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${p.name}</div>
                <div style="color:#f4f4f5;font-size:13px;font-weight:600">+${Number(p.value).toLocaleString()}% growth</div>`;
      },
    },
    grid: { left: 8, right: 48, bottom: 8, top: 8, containLabel: true },
    xAxis: {
      type: 'value',
      axisLabel: { ...AXIS_LABEL, formatter: (v: number) => `+${v >= 1000 ? `${(v / 1000).toFixed(1)}k` : v}%` },
      splitLine: SPLIT_LINE,
    },
    yAxis: {
      type: 'category',
      data: sorted.map(t => t.term),
      axisLabel: { ...AXIS_LABEL, fontSize: 10, width: 110, overflow: 'truncate' },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [{
      type: 'bar',
      data: sorted.map(t => t.percent_gain),
      barMaxWidth: 18,
      itemStyle: {
        color: {
          type: 'linear', x: 0, y: 0, x2: 1, y2: 0,
          colorStops: [
            { offset: 0, color: '#92400e' },
            { offset: 1, color: '#d97706' },
          ],
        },
        borderRadius: [0, 4, 4, 0],
      },
      label: {
        show: true, position: 'right', color: '#a1a1aa', fontSize: 9,
        fontFamily: 'JetBrains Mono, monospace',
        formatter: (p: ChartDatum) => `+${Number(p.value).toLocaleString()}%`,
      },
    }],
  };
}

function geoBarOption(geo: { country_code: string; country_name: string; score: number }[], currentCode: string) {
  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      ...CHART_TOOLTIP,
      formatter: (params: ChartDatum[]) => {
        const p = params[0];
        return `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${p.name}</div>
                <div style="color:#f4f4f5;font-size:13px;font-weight:600">score ${p.value} / 100</div>`;
      },
    },
    grid: { left: 40, right: 16, bottom: 64, top: 16 },
    xAxis: {
      type: 'category',
      data: geo.map(g => g.country_name),
      // width+truncate keeps long Nielsen DMA names (US view) readable.
      axisLabel: { ...AXIS_LABEL, fontSize: 9, rotate: 35, width: 100, overflow: 'truncate' },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    yAxis: {
      type: 'value',
      max: 100,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    series: [{
      type: 'bar',
      data: geo.map(g => ({
        value: g.score,
        // The country currently selected in the filter is emphasized.
        itemStyle: g.country_code === currentCode ? {
          color: '#38bdf8',
          borderRadius: [4, 4, 0, 0],
        } : {
          color: {
            type: 'linear', x: 0, y: 0, x2: 0, y2: 1,
            colorStops: [
              { offset: 0, color: '#0284c7' },
              { offset: 1, color: '#164e63' },
            ],
          },
          borderRadius: [4, 4, 0, 0],
        },
      })),
      barMaxWidth: 22,
    }],
  };
}

function historyOption(history: { term: string; week: string; score: number }[], termColors: Record<string, string>) {
  const byTerm = new Map<string, [string, number][]>();
  for (const p of history) {
    if (!byTerm.has(p.term)) byTerm.set(p.term, []);
    byTerm.get(p.term)!.push([p.week, p.score]);
  }

  // History rows come back lowercased-matched; color lookup is case-insensitive.
  const colorFor = (term: string) => {
    const match = Object.keys(termColors).find(t => t.toLowerCase() === term.toLowerCase());
    return match ? termColors[match] : SERIES_COLORS[0];
  };

  const series = [...byTerm.entries()].map(([term, points]) => ({
    name: term,
    type: 'line',
    data: points,
    showSymbol: false,
    smooth: true,
    lineStyle: { color: colorFor(term), width: 2 },
    itemStyle: { color: colorFor(term) },
    emphasis: { focus: 'series' },
    endLabel: {
      show: true,
      formatter: ({ seriesName }: SeriesLabelParam) => seriesName.length > 14 ? `${seriesName.slice(0, 13)}…` : seriesName,
      color: colorFor(term),
      fontSize: 10,
      distance: 6,
    },
    labelLayout: { hideOverlap: true },
  }));

  return {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
    legend: {
      show: series.length > 1,
      top: 0,
      textStyle: { color: '#a1a1aa', fontSize: 10 },
      icon: 'circle',
      itemWidth: 8,
      itemHeight: 8,
    },
    grid: { left: 40, right: 110, bottom: 56, top: series.length > 1 ? 32 : 16 },
    xAxis: {
      type: 'time',
      axisLabel: AXIS_LABEL,
      axisLine: { show: false },
      axisTick: { show: false },
    },
    yAxis: {
      type: 'value',
      max: 100,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    dataZoom: [
      { type: 'inside', start: 0, end: 100 },
      { type: 'slider', height: 16, bottom: 8, borderColor: '#27272a', backgroundColor: '#09090b', fillerColor: 'rgba(56,189,248,0.08)', textStyle: { color: '#71717a', fontSize: 9 } },
    ],
    series,
  };
}

// --- Local filter controls ---

function CountryCombobox({ label, countries, value, onChange }: {
  label: string;
  countries: { name: string; code: string }[];
  value: string;
  onChange: (code: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const ref = React.useRef<HTMLDivElement>(null);

  const selected = countries.find(c => c.code === value);
  const filtered = query
    ? countries.filter(c => c.name.toLowerCase().includes(query.toLowerCase()) || c.code.toLowerCase().includes(query.toLowerCase()))
    : countries;

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, []);

  return (
    <div ref={ref} className="relative">
      <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">{label}</label>
      <button
        onClick={() => { setOpen(!open); setQuery(''); }}
        className="text-xs text-zinc-400 rounded-lg pl-3 pr-8 py-2 text-left border border-zinc-800/50 transition-colors hover:border-zinc-700/60 cursor-pointer relative"
        style={{ background: '#09090b', minWidth: 180 }}
      >
        {selected ? selected.name : `Select ${label.toLowerCase()}`}
        <ChevronDown size={12} className={`absolute right-2.5 top-1/2 -translate-y-1/2 text-zinc-600 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>

      {open && (
        <div className="absolute z-50 mt-1 w-64 rounded-lg border border-zinc-800/60 shadow-xl overflow-hidden" style={{ background: '#111114' }}>
          <div className="p-1.5">
            <input
              autoFocus
              value={query}
              onChange={e => setQuery(e.target.value)}
              placeholder="Type to filter..."
              className="w-full text-xs text-zinc-300 rounded-md px-2.5 py-1.5 outline-none border border-zinc-800/50 focus:border-cyan-500/30 placeholder:text-zinc-700"
              style={{ background: '#09090b' }}
            />
          </div>
          <div className="max-h-56 overflow-y-auto">
            {filtered.length > 0 ? filtered.map(c => (
              <button
                key={c.code}
                onClick={() => { onChange(c.code); setOpen(false); setQuery(''); }}
                className={`w-full flex items-center justify-between text-left px-3 py-1.5 text-xs cursor-pointer transition-colors ${
                  c.code === value ? 'text-cyan-400 bg-cyan-500/5' : 'text-zinc-400 hover:bg-zinc-800/50 hover:text-zinc-200'
                }`}
              >
                <span>{c.name}</span>
                {/* DMA entries use the name as their code — no point showing it twice. */}
                {c.code !== '' && c.code !== c.name && (
                  <span className="font-mono text-[10px] text-zinc-600">{c.code}</span>
                )}
              </button>
            )) : (
              <p className="px-3 py-2 text-[11px] text-zinc-600">No matches</p>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function TermSearchBox({ topTerms, risingTerms, onSelect }: {
  topTerms: string[];
  risingTerms: string[];
  onSelect: (term: string) => void;
}) {
  const [query, setQuery] = useState('');
  const [open, setOpen] = useState(false);
  const ref = React.useRef<HTMLDivElement>(null);

  const candidates = useMemo(() => {
    const seen = new Map<string, 'top' | 'rising'>();
    topTerms.forEach(t => seen.set(t, 'top'));
    risingTerms.forEach(t => { if (!seen.has(t)) seen.set(t, 'rising'); });
    return [...seen.entries()];
  }, [topTerms, risingTerms]);

  const matches = query
    ? candidates.filter(([t]) => t.toLowerCase().includes(query.toLowerCase())).slice(0, 8)
    : [];

  function submit(term: string) {
    onSelect(term);
    setQuery('');
    setOpen(false);
  }

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, []);

  return (
    <div ref={ref} className="relative">
      <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">Search Term</label>
      <div className="relative">
        <Search size={12} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-zinc-600 pointer-events-none" />
        <input
          value={query}
          onChange={e => { setQuery(e.target.value); setOpen(true); }}
          onFocus={() => setOpen(true)}
          onKeyDown={e => {
            if (e.key === 'Enter' && query.trim()) {
              submit(matches.length > 0 ? matches[0][0] : query.trim());
            } else if (e.key === 'Escape') {
              setOpen(false);
            }
          }}
          placeholder="Search a term, press Enter..."
          className="text-xs text-zinc-300 rounded-lg pl-7 pr-3 py-2 outline-none border border-zinc-800/50 transition-colors focus:border-cyan-500/30 placeholder:text-zinc-700"
          style={{ background: '#09090b', minWidth: 210 }}
        />
      </div>

      {open && query && (
        <div className="absolute z-50 mt-1 w-full rounded-lg border border-zinc-800/60 shadow-xl overflow-hidden" style={{ background: '#111114' }}>
          {matches.map(([term, source]) => (
            <button
              key={term}
              onClick={() => submit(term)}
              className="w-full flex items-center justify-between text-left px-3 py-1.5 text-xs cursor-pointer text-zinc-400 hover:bg-zinc-800/50 hover:text-zinc-200 transition-colors"
            >
              <span className="truncate">{term}</span>
              <span className={`flex items-center gap-1 text-[9px] font-semibold uppercase shrink-0 ml-2 ${
                source === 'top' ? 'text-cyan-500' : 'text-amber-500'
              }`}>
                {source === 'top' ? <TrendingUp size={9} /> : <Flame size={9} />}
                {source}
              </span>
            </button>
          ))}
          {/* Free search: any term can be mapped/charted, not just today's list */}
          <button
            onClick={() => submit(query.trim())}
            className={`w-full flex items-center gap-2 text-left px-3 py-1.5 text-xs cursor-pointer text-zinc-400 hover:bg-zinc-800/50 hover:text-zinc-200 transition-colors ${
              matches.length > 0 ? 'border-t border-zinc-800/60' : ''
            }`}
          >
            <Search size={10} className="shrink-0 text-zinc-600" />
            <span className="truncate">
              Search "<span className="text-zinc-200">{query.trim()}</span>" everywhere
            </span>
          </button>
          {matches.length === 0 && (
            <p className="px-3 pb-2 text-[10px] text-zinc-600">Not in today's top or rising charts — Enter searches it anyway</p>
          )}
        </div>
      )}
    </div>
  );
}

function LoadingPulse() {
  return (
    <div className="space-y-4">
      {[1, 2, 3].map(i => (
        <div key={i} className="h-32 rounded-2xl animate-pulse" style={{ background: '#111114' }} />
      ))}
    </div>
  );
}

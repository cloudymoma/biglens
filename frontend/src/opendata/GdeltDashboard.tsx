import { useState, useEffect, useMemo } from 'react';
import ReactECharts from 'echarts-for-react';
import * as echarts from 'echarts';
import { Activity, Thermometer, Scale, Swords, ExternalLink } from 'lucide-react';
import type {
  GdeltEventsData, GdeltGkgData, GdeltHotspot, GdeltDaily, GdeltQuadClass,
  GdeltEventType, GdeltNews, GdeltNamedCount, GdeltMediaSource,
} from '../types';
import { fetchGdeltEvents, fetchGdeltGkg } from '../api';
import { MetricCard, EmptyState, ErrorBanner } from '../dashboards/shared';

// Span caps mirror the backend limits (events <= 90 days, GKG <= 30 days).
const MAX_EVENTS_DAYS = 90;
const MAX_GKG_DAYS = 30;

const CHART_TOOLTIP = {
  backgroundColor: 'rgba(17,17,20,0.95)',
  borderColor: '#27272a',
  textStyle: { color: '#e4e4e7', fontSize: 12 },
};

const AXIS_LABEL = { color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' };
const SPLIT_LINE = { lineStyle: { color: '#1f1f23', type: 'dashed' } };

// Labels are a frontend concern: the API returns raw GDELT codes.
const QUAD_LABELS: Record<number, string> = {
  1: 'Verbal Cooperation',
  2: 'Material Cooperation',
  3: 'Verbal Conflict',
  4: 'Material Conflict',
};

// CAMEO event root codes ('01'-'20'); codes >= '10' are the conflict side.
const CAMEO_LABELS: Record<string, string> = {
  '01': 'Public Statement', '02': 'Appeal', '03': 'Express Intent to Cooperate',
  '04': 'Consult', '05': 'Diplomatic Cooperation', '06': 'Material Cooperation',
  '07': 'Provide Aid', '08': 'Yield', '09': 'Investigate', '10': 'Demand',
  '11': 'Disapprove', '12': 'Reject', '13': 'Threaten', '14': 'Protest',
  '15': 'Exhibit Force Posture', '16': 'Reduce Relations', '17': 'Coerce',
  '18': 'Assault', '19': 'Fight', '20': 'Mass Violence',
};

const cameoLabel = (code: string) => CAMEO_LABELS[code] || `Code ${code}`;

// Tone scale: <= -2 alarming, -2..2 neutral, >= 2 positive.
const toneColor = (t: number) => (t <= -2 ? '#ef4444' : t < 2 ? '#fbbf24' : '#22c55e');

// GDELT dates are UTC (_PARTITIONDATE); all range math sticks to UTC.
const fmtDate = (d: Date) => d.toISOString().slice(0, 10);
const daysAgoUTC = (n: number) => fmtDate(new Date(Date.now() - n * 86400000));

const spanOf = (start: string, end: string) =>
  Math.round((Date.parse(end) - Date.parse(start)) / 86400000) + 1;

const PRESETS = [
  { label: '3 days', days: 3 },
  { label: '7 days', days: 7 },
  { label: '30 days', days: 30 },
];

export default function GdeltDashboard() {
  const [startDate, setStartDate] = useState(daysAgoUTC(2));
  const [endDate, setEndDate] = useState(daysAgoUTC(0));

  const [events, setEvents] = useState<GdeltEventsData | null>(null);
  const [eventsLoading, setEventsLoading] = useState(true);
  const [eventsError, setEventsError] = useState('');

  const [gkg, setGkg] = useState<GdeltGkgData | null>(null);
  const [gkgLoading, setGkgLoading] = useState(true);
  const [gkgError, setGkgError] = useState('');

  const [mapReady, setMapReady] = useState(false);

  const today = daysAgoUTC(0);
  const span = spanOf(startDate, endDate);
  const rangeError = span < 1
    ? 'Start date must be on or before end date.'
    : span > MAX_EVENTS_DAYS
      ? `Date range spans ${span} days; at most ${MAX_EVENTS_DAYS} days are supported.`
      : '';
  const gkgTooWide = !rangeError && span > MAX_GKG_DAYS;

  // ECharts 6 ships no built-in maps; the world outline is a vendored
  // Natural Earth 110m GeoJSON registered once per app lifetime.
  useEffect(() => {
    if (echarts.getMap('world')) {
      setMapReady(true);
      return;
    }
    fetch('/maps/world.geo.json')
      .then(r => r.json())
      .then(geoJson => {
        echarts.registerMap('world', geoJson);
        setMapReady(true);
      })
      .catch(() => setMapReady(false));
  }, []);

  useEffect(() => {
    if (rangeError) return;
    setEventsLoading(true);
    setEventsError('');
    fetchGdeltEvents(startDate, endDate)
      .then(setEvents)
      .catch(e => setEventsError(e.response?.data || e.message))
      .finally(() => setEventsLoading(false));
  }, [startDate, endDate, rangeError]);

  // The GKG panel loads independently so heavy theme parsing never blocks
  // the event panels.
  useEffect(() => {
    if (rangeError || gkgTooWide) return;
    setGkgLoading(true);
    setGkgError('');
    fetchGdeltGkg(startDate, endDate)
      .then(setGkg)
      .catch(e => setGkgError(e.response?.data || e.message))
      .finally(() => setGkgLoading(false));
  }, [startDate, endDate, rangeError, gkgTooWide]);

  const conflictShare = useMemo(() => {
    if (!events || events.overall.event_count === 0) return 0;
    const conflict = events.quad_class
      .filter(q => q.quad_class >= 3)
      .reduce((s, q) => s + q.event_count, 0);
    return Math.round((conflict / events.overall.event_count) * 100);
  }, [events]);

  function setPreset(days: number) {
    setStartDate(daysAgoUTC(days - 1));
    setEndDate(daysAgoUTC(0));
  }

  return (
    <div className="space-y-6">
      {/* Filter bar: quick presets + custom UTC date range */}
      <div className="rounded-2xl border border-zinc-800/50 p-4 flex flex-wrap items-end gap-4" style={{ background: '#111114' }}>
        <div>
          <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">Quick Range</label>
          <div className="flex gap-1.5">
            {PRESETS.map(p => {
              const active = startDate === daysAgoUTC(p.days - 1) && endDate === today;
              return (
                <button
                  key={p.days}
                  onClick={() => setPreset(p.days)}
                  className={`text-xs rounded-lg px-3 py-2 border cursor-pointer transition-colors ${
                    active
                      ? 'border-cyan-500/30 bg-cyan-500/5 text-cyan-400'
                      : 'border-zinc-800/50 text-zinc-400 hover:border-zinc-700/60'
                  }`}
                  style={{ background: active ? undefined : '#09090b' }}
                >
                  {p.label}
                </button>
              );
            })}
          </div>
        </div>
        <DateInput label="From" value={startDate} max={endDate <= today ? endDate : today} onChange={setStartDate} />
        <DateInput label="To" value={endDate} min={startDate} max={today} onChange={setEndDate} />
        <p className="text-[10px] text-zinc-600 ml-auto self-center max-w-[260px] leading-relaxed">
          Dates reported in UTC. GDELT refreshes every 15 minutes; events support {MAX_EVENTS_DAYS} days,
          themes &amp; entities {MAX_GKG_DAYS} days.
        </p>
      </div>

      {rangeError && <ErrorBanner message={rangeError} />}
      {eventsError && <ErrorBanner message={eventsError} />}
      {!rangeError && eventsLoading && <LoadingPulse />}

      {!rangeError && !eventsLoading && !eventsError && events && (
        <>
          {/* Summary metrics (weighted server-side) */}
          <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
            <MetricCard label="Events Reported" value={events.overall.event_count.toLocaleString()}
              icon={<Activity size={18} />} detail={`${startDate} → ${endDate}`} accentColor="#38bdf8" />
            <MetricCard label="Global Tone" value={events.overall.avg_tone.toFixed(2)}
              icon={<Thermometer size={18} />} detail="Weighted avg, −10 to +10" accentColor={toneColor(events.overall.avg_tone)} />
            <MetricCard label="Goldstein Scale" value={events.overall.avg_goldstein.toFixed(2)}
              icon={<Scale size={18} />} detail="Cooperation −10 … +10" accentColor="#a78bfa" />
            <MetricCard label="Conflict Share" value={`${conflictShare}%`}
              icon={<Swords size={18} />} detail="Verbal + material conflict" accentColor="#f43f5e" />
          </div>

          {/* Panel 1: global situation awareness */}
          <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
            <h3 className="text-sm font-semibold text-white mb-1">Global Event Hotspots</h3>
            <p className="text-xs text-zinc-500 mb-4">
              Top {events.hotspots.length} locations · bubble size = events, color = tone · drag to pan, scroll to zoom
            </p>
            {mapReady && events.hotspots.length > 0 ? (
              <div className="h-[440px]">
                <ReactECharts option={geoOption(events.hotspots)} style={{ height: '100%' }} notMerge />
              </div>
            ) : mapReady ? (
              <EmptyState text="No geolocated events in this range" />
            ) : (
              <div className="h-[440px] rounded-xl animate-pulse" style={{ background: '#09090b' }} />
            )}
          </div>

          <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
            <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
              <h3 className="text-sm font-semibold text-white mb-1">Sentiment Gauge</h3>
              <p className="text-xs text-zinc-500 mb-2">Weighted global tone</p>
              <div className="h-[260px]">
                <ReactECharts option={gaugeOption(events.overall.avg_tone)} style={{ height: '100%' }} />
              </div>
            </div>
            <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
              <h3 className="text-sm font-semibold text-white mb-1">Volume &amp; Tone Trend</h3>
              <p className="text-xs text-zinc-500 mb-2">Daily events (bars) vs tone (line)</p>
              {events.daily.length > 0 ? (
                <div className="h-[260px]">
                  <ReactECharts option={dailyOption(events.daily)} style={{ height: '100%' }} />
                </div>
              ) : (
                <EmptyState text="No daily data" />
              )}
            </div>
            <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
              <h3 className="text-sm font-semibold text-white mb-1">Cooperation vs Conflict</h3>
              <p className="text-xs text-zinc-500 mb-2">Event mix by QuadClass</p>
              {events.quad_class.length > 0 ? (
                <div className="h-[260px]">
                  <ReactECharts option={quadOption(events.quad_class)} style={{ height: '100%' }} />
                </div>
              ) : (
                <EmptyState text="No data" />
              )}
            </div>
          </div>

          {/* Panel 2: geopolitical risk monitor */}
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
              <h3 className="text-sm font-semibold text-white mb-1">Risk Matrix</h3>
              <p className="text-xs text-zinc-500 mb-4">
                Event types: Goldstein score (x) vs activity (y) — lower-right = high-volume destabilizing
              </p>
              {events.event_types.length > 0 ? (
                <div className="h-[340px]">
                  <ReactECharts option={riskScatterOption(events.event_types)} style={{ height: '100%' }} />
                </div>
              ) : (
                <EmptyState text="No event types" />
              )}
            </div>
            <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
              <h3 className="text-sm font-semibold text-white mb-1">Conflict Categories</h3>
              <p className="text-xs text-zinc-500 mb-4">Events per CAMEO conflict root code</p>
              {events.event_types.some(t => t.event_root_code >= '10' && t.event_root_code <= '20') ? (
                <div className="h-[340px]">
                  <ReactECharts option={conflictBarOption(events.event_types)} style={{ height: '100%' }} />
                </div>
              ) : (
                <EmptyState text="No conflict events" />
              )}
            </div>
          </div>

          <NewsTable news={events.conflict_news} />
        </>
      )}

      {/* Panel 3: themes & entities (GKG) — independent lifecycle */}
      {!rangeError && (
        <div className="space-y-6">
          <div className="flex items-baseline gap-3">
            <h2 className="text-sm font-semibold text-white">Themes &amp; Entities</h2>
            <span className="text-[10px] text-zinc-600">GDELT Global Knowledge Graph · loads independently</span>
          </div>

          {gkgTooWide ? (
            <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
              <EmptyState text={`Theme & entity analysis supports up to ${MAX_GKG_DAYS} days — narrow the range to load this panel`} />
            </div>
          ) : gkgError ? (
            <ErrorBanner message={gkgError} />
          ) : gkgLoading ? (
            <LoadingPulse />
          ) : gkg && (
            <>
              <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
                <h3 className="text-sm font-semibold text-white mb-1">Trending Themes</h3>
                <p className="text-xs text-zinc-500 mb-4">Top 50 GKG themes by article count</p>
                {gkg.themes.length > 0 ? (
                  <div className="h-[380px]">
                    <ReactECharts option={treemapOption(gkg.themes)} style={{ height: '100%' }} />
                  </div>
                ) : (
                  <EmptyState text="No themes in this range" />
                )}
              </div>

              <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
                <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
                  <h3 className="text-sm font-semibold text-white mb-1">Most Covered People</h3>
                  <p className="text-xs text-zinc-500 mb-4">Top 20 by article count</p>
                  {gkg.persons.length > 0 ? (
                    <div className="h-[420px]">
                      <ReactECharts option={personsBarOption(gkg.persons)} style={{ height: '100%' }} />
                    </div>
                  ) : (
                    <EmptyState text="No people in this range" />
                  )}
                </div>
                <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
                  <h3 className="text-sm font-semibold text-white mb-1">Leading Media Sources</h3>
                  <p className="text-xs text-zinc-500 mb-4">Top 10 by volume, colored by average tone</p>
                  {gkg.sources.length > 0 ? (
                    <div className="h-[420px]">
                      <ReactECharts option={sourcesBarOption(gkg.sources)} style={{ height: '100%' }} />
                    </div>
                  ) : (
                    <EmptyState text="No sources in this range" />
                  )}
                </div>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
}

// --- News table ---

// SOURCEURL is untrusted external data: only http/https URLs become links.
function safeUrl(raw: string): URL | null {
  try {
    const u = new URL(raw);
    return u.protocol === 'http:' || u.protocol === 'https:' ? u : null;
  } catch {
    return null;
  }
}

function NewsTable({ news }: { news: GdeltNews[] }) {
  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <h3 className="text-sm font-semibold text-white mb-1">Breaking Conflict Reports</h3>
      <p className="text-xs text-zinc-500 mb-4">Top {news.length} most-mentioned reports (one row per article)</p>
      {news.length > 0 ? (
        <div className="max-h-[420px] overflow-y-auto">
          <table className="w-full text-xs">
            <thead className="sticky top-0" style={{ background: '#111114' }}>
              <tr className="text-left text-[10px] font-mono text-zinc-600 uppercase">
                <th className="py-2 pr-3 font-medium">Reported</th>
                <th className="py-2 pr-3 font-medium">Country</th>
                <th className="py-2 pr-3 font-medium">Type</th>
                <th className="py-2 pr-3 font-medium text-right">Tone</th>
                <th className="py-2 pr-3 font-medium text-right">Mentions</th>
                <th className="py-2 font-medium">Source</th>
              </tr>
            </thead>
            <tbody>
              {news.map((n, i) => {
                const url = safeUrl(n.source_url);
                return (
                  <tr key={`${n.source_url}-${i}`} className="border-t border-zinc-800/40 text-zinc-400">
                    <td className="py-2 pr-3 font-mono text-[11px] whitespace-nowrap">{n.ingest_date}</td>
                    <td className="py-2 pr-3 font-mono text-[11px]">{n.fips_country}</td>
                    <td className="py-2 pr-3">{cameoLabel(n.event_root_code)}</td>
                    <td className="py-2 pr-3 text-right font-mono" style={{ color: toneColor(n.avg_tone) }}>
                      {n.avg_tone.toFixed(1)}
                    </td>
                    <td className="py-2 pr-3 text-right font-mono">{n.mention_count.toLocaleString()}</td>
                    <td className="py-2 max-w-[280px]">
                      {url ? (
                        <a
                          href={url.href}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="flex items-center gap-1 text-cyan-500 hover:text-cyan-300 truncate transition-colors"
                          title={url.href}
                        >
                          <span className="truncate">{url.hostname}</span>
                          <ExternalLink size={10} className="shrink-0" />
                        </a>
                      ) : (
                        <span className="text-zinc-600 truncate block" title={n.source_url}>{n.source_url}</span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : (
        <EmptyState text="No conflict reports in this range" />
      )}
    </div>
  );
}

// --- Chart options ---

interface GeoScatterParam { value: [number, number, number, number]; }

function geoOption(hotspots: GdeltHotspot[]) {
  const maxCount = Math.max(...hotspots.map(h => h.event_count), 1);
  return {
    backgroundColor: 'transparent',
    tooltip: {
      ...CHART_TOOLTIP,
      formatter: (p: GeoScatterParam) =>
        `<div style="color:#f4f4f5;font-size:12px;font-weight:600">${p.value[2].toLocaleString()} events</div>
         <div style="color:#a1a1aa;font-size:11px">tone ${p.value[3]} · ${p.value[1]}, ${p.value[0]}</div>`,
    },
    visualMap: {
      min: -8,
      max: 8,
      dimension: 3,
      orient: 'horizontal',
      left: 'center',
      bottom: 0,
      itemWidth: 10,
      itemHeight: 90,
      text: ['Positive tone', 'Negative tone'],
      textStyle: { color: '#71717a', fontSize: 9 },
      inRange: { color: ['#ef4444', '#a1a1aa', '#22c55e'] },
    },
    geo: {
      map: 'world',
      roam: true,
      zoom: 1.2,
      itemStyle: { areaColor: '#18181b', borderColor: '#27272a' },
      emphasis: { itemStyle: { areaColor: '#1f1f23' }, label: { show: false } },
      select: { disabled: true },
    },
    series: [{
      type: 'scatter',
      coordinateSystem: 'geo',
      data: hotspots.map(h => [h.longitude, h.latitude, h.event_count, h.avg_tone]),
      symbolSize: (v: number[]) => 3 + 16 * Math.sqrt(v[2] / maxCount),
      itemStyle: { opacity: 0.75 },
    }],
  };
}

function gaugeOption(tone: number) {
  return {
    backgroundColor: 'transparent',
    series: [{
      type: 'gauge',
      min: -10,
      max: 10,
      startAngle: 200,
      endAngle: -20,
      splitNumber: 4,
      axisLine: {
        lineStyle: {
          width: 14,
          // -10..-2 alarming, -2..2 neutral, 2..10 positive.
          color: [[0.4, '#ef4444'], [0.6, '#fbbf24'], [1, '#22c55e']],
        },
      },
      pointer: { itemStyle: { color: '#e4e4e7' }, width: 4 },
      axisTick: { show: false },
      splitLine: { length: 10, lineStyle: { color: '#09090b', width: 2 } },
      axisLabel: { color: '#71717a', fontSize: 9, distance: 20, fontFamily: 'JetBrains Mono, monospace' },
      detail: {
        valueAnimation: true,
        formatter: (v: number) => v.toFixed(2),
        color: toneColor(tone),
        fontSize: 24,
        fontFamily: 'JetBrains Mono, monospace',
        offsetCenter: [0, '65%'],
      },
      data: [{ value: tone }],
    }],
  };
}

function dailyOption(daily: GdeltDaily[]) {
  return {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
    legend: { top: 0, textStyle: { color: '#a1a1aa', fontSize: 10 }, icon: 'circle', itemWidth: 8, itemHeight: 8 },
    grid: { left: 8, right: 8, bottom: 8, top: 30, containLabel: true },
    xAxis: {
      type: 'category',
      data: daily.map(d => d.ingest_date.slice(5)),
      axisLabel: { ...AXIS_LABEL, fontSize: 9 },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    yAxis: [
      { type: 'value', name: 'events', nameTextStyle: AXIS_LABEL, axisLabel: { ...AXIS_LABEL, formatter: (v: number) => (v >= 1000 ? `${v / 1000}k` : v) }, splitLine: SPLIT_LINE },
      { type: 'value', name: 'tone', nameTextStyle: AXIS_LABEL, axisLabel: AXIS_LABEL, splitLine: { show: false } },
    ],
    series: [
      {
        name: 'Events',
        type: 'bar',
        data: daily.map(d => d.event_count),
        barMaxWidth: 20,
        itemStyle: { color: 'rgba(2,132,199,0.55)', borderRadius: [3, 3, 0, 0] },
      },
      {
        name: 'Tone',
        type: 'line',
        yAxisIndex: 1,
        data: daily.map(d => d.avg_tone),
        smooth: true,
        showSymbol: daily.length <= 31,
        lineStyle: { color: '#fbbf24', width: 2 },
        itemStyle: { color: '#fbbf24' },
      },
    ],
  };
}

function quadOption(quads: GdeltQuadClass[]) {
  const COLORS: Record<number, string> = { 1: '#0284c7', 2: '#059669', 3: '#d97706', 4: '#dc2626' };
  return {
    backgroundColor: 'transparent',
    tooltip: { ...CHART_TOOLTIP },
    legend: { bottom: 0, textStyle: { color: '#a1a1aa', fontSize: 9 }, icon: 'circle', itemWidth: 8, itemHeight: 8 },
    series: [{
      type: 'pie',
      radius: ['46%', '68%'],
      center: ['50%', '44%'],
      data: quads.map(q => ({
        name: QUAD_LABELS[q.quad_class] || `Class ${q.quad_class}`,
        value: q.event_count,
        itemStyle: { color: COLORS[q.quad_class] || '#71717a' },
      })),
      label: { color: '#a1a1aa', fontSize: 9, formatter: '{d}%' },
      itemStyle: { borderColor: '#111114', borderWidth: 2 },
    }],
  };
}

interface RiskScatterParam { data: { name: string; value: [number, number, number] } }

function riskScatterOption(types: GdeltEventType[]) {
  const maxCount = Math.max(...types.map(t => t.event_count), 1);
  return {
    backgroundColor: 'transparent',
    tooltip: {
      ...CHART_TOOLTIP,
      formatter: (p: RiskScatterParam) =>
        `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${cameoLabel(p.data.name)}</div>
         <div style="color:#f4f4f5;font-size:12px">${p.data.value[1].toLocaleString()} events</div>
         <div style="color:#a1a1aa;font-size:11px">Goldstein ${p.data.value[0]} · tone ${p.data.value[2]}</div>`,
    },
    grid: { left: 8, right: 24, bottom: 8, top: 16, containLabel: true },
    xAxis: {
      type: 'value',
      name: 'Goldstein',
      nameTextStyle: AXIS_LABEL,
      min: -10,
      max: 10,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    yAxis: {
      type: 'log',
      name: 'events',
      nameTextStyle: AXIS_LABEL,
      axisLabel: { ...AXIS_LABEL, formatter: (v: number) => (v >= 1000 ? `${v / 1000}k` : v) },
      splitLine: SPLIT_LINE,
    },
    series: [{
      type: 'scatter',
      data: types.map(t => ({
        name: t.event_root_code,
        value: [t.avg_goldstein, t.event_count, t.avg_tone],
        // Destabilizing types (negative Goldstein) surface in red.
        itemStyle: { color: t.avg_goldstein < 0 ? '#ef4444' : '#0284c7', opacity: 0.8 },
      })),
      symbolSize: (v: number[]) => 8 + 22 * Math.sqrt(v[1] / maxCount),
      label: {
        show: true,
        position: 'top',
        fontSize: 8,
        color: '#71717a',
        formatter: (p: RiskScatterParam) => cameoLabel(p.data.name),
      },
      labelLayout: { hideOverlap: true },
      markLine: {
        silent: true,
        symbol: 'none',
        lineStyle: { color: '#3f3f46', type: 'dashed' },
        label: { show: false },
        data: [{ xAxis: 0 }],
      },
    }],
  };
}

function conflictBarOption(types: GdeltEventType[]) {
  const conflict = types
    .filter(t => t.event_root_code >= '10' && t.event_root_code <= '20')
    .sort((a, b) => a.event_count - b.event_count);
  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      ...CHART_TOOLTIP,
      formatter: (params: { name: string; value: number }[]) => {
        const p = params[0];
        return `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${p.name}</div>
                <div style="color:#f4f4f5;font-size:13px;font-weight:600">${Number(p.value).toLocaleString()} events</div>`;
      },
    },
    grid: { left: 8, right: 48, bottom: 8, top: 8, containLabel: true },
    xAxis: {
      type: 'value',
      axisLabel: { ...AXIS_LABEL, formatter: (v: number) => (v >= 1000 ? `${v / 1000}k` : v) },
      splitLine: SPLIT_LINE,
    },
    yAxis: {
      type: 'category',
      data: conflict.map(t => cameoLabel(t.event_root_code)),
      axisLabel: { ...AXIS_LABEL, fontSize: 10, width: 130, overflow: 'truncate' },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [{
      type: 'bar',
      data: conflict.map(t => t.event_count),
      barMaxWidth: 16,
      itemStyle: {
        color: {
          type: 'linear', x: 0, y: 0, x2: 1, y2: 0,
          colorStops: [
            { offset: 0, color: '#7f1d1d' },
            { offset: 1, color: '#dc2626' },
          ],
        },
        borderRadius: [0, 4, 4, 0],
      },
      label: {
        show: true, position: 'right', color: '#a1a1aa', fontSize: 9,
        fontFamily: 'JetBrains Mono, monospace',
        formatter: (p: { value: number }) => Number(p.value).toLocaleString(),
      },
    }],
  };
}

// GKG theme codes read like TAX_FNCACT_PRESIDENT / WB_2670_JOBS; drop the
// numeric WB prefix and underscores for display, keep the raw code in the
// tooltip.
function prettyTheme(name: string) {
  return name.replace(/^WB_\d+_/, '').replace(/_/g, ' ');
}

const TREEMAP_COLORS = ['#0284c7', '#059669', '#d97706', '#8b5cf6', '#f43f5e', '#0891b2', '#65a30d', '#c026d3'];

function treemapOption(themes: GdeltNamedCount[]) {
  return {
    backgroundColor: 'transparent',
    tooltip: {
      ...CHART_TOOLTIP,
      formatter: (p: { data: { rawName: string }; value: number }) =>
        `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${p.data.rawName}</div>
         <div style="color:#f4f4f5;font-size:13px;font-weight:600">${Number(p.value).toLocaleString()} articles</div>`,
    },
    series: [{
      type: 'treemap',
      roam: false,
      nodeClick: false,
      breadcrumb: { show: false },
      width: '100%',
      height: '100%',
      data: themes.map((t, i) => ({
        name: prettyTheme(t.name),
        rawName: t.name,
        value: t.article_count,
        itemStyle: { color: TREEMAP_COLORS[i % TREEMAP_COLORS.length], colorAlpha: 0.85 },
      })),
      label: { color: '#f4f4f5', fontSize: 10, overflow: 'truncate' },
      itemStyle: { borderColor: '#111114', borderWidth: 1.5, gapWidth: 1.5 },
    }],
  };
}

function personsBarOption(persons: GdeltNamedCount[]) {
  const sorted = [...persons].sort((a, b) => a.article_count - b.article_count);
  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      ...CHART_TOOLTIP,
      formatter: (params: { name: string; value: number }[]) => {
        const p = params[0];
        return `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${p.name}</div>
                <div style="color:#f4f4f5;font-size:13px;font-weight:600">${Number(p.value).toLocaleString()} articles</div>`;
      },
    },
    grid: { left: 8, right: 56, bottom: 8, top: 8, containLabel: true },
    xAxis: {
      type: 'value',
      axisLabel: { ...AXIS_LABEL, formatter: (v: number) => (v >= 1000 ? `${v / 1000}k` : v) },
      splitLine: SPLIT_LINE,
    },
    yAxis: {
      type: 'category',
      data: sorted.map(p => p.name),
      axisLabel: { ...AXIS_LABEL, fontSize: 10, width: 120, overflow: 'truncate' },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [{
      type: 'bar',
      data: sorted.map(p => p.article_count),
      barMaxWidth: 14,
      itemStyle: {
        color: {
          type: 'linear', x: 0, y: 0, x2: 1, y2: 0,
          colorStops: [
            { offset: 0, color: '#4c1d95' },
            { offset: 1, color: '#8b5cf6' },
          ],
        },
        borderRadius: [0, 4, 4, 0],
      },
      label: {
        show: true, position: 'right', color: '#a1a1aa', fontSize: 9,
        fontFamily: 'JetBrains Mono, monospace',
        formatter: (p: { value: number }) => Number(p.value).toLocaleString(),
      },
    }],
  };
}

interface SourceBarParam { data: { value: number; tone: number }; name: string }

function sourcesBarOption(sources: GdeltMediaSource[]) {
  const sorted = [...sources].sort((a, b) => a.article_count - b.article_count);
  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      ...CHART_TOOLTIP,
      formatter: (params: SourceBarParam[]) => {
        const p = params[0];
        return `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${p.name}</div>
                <div style="color:#f4f4f5;font-size:13px;font-weight:600">${p.data.value.toLocaleString()} articles</div>
                <div style="color:${toneColor(p.data.tone)};font-size:11px">avg tone ${p.data.tone.toFixed(2)}</div>`;
      },
    },
    grid: { left: 8, right: 72, bottom: 8, top: 8, containLabel: true },
    xAxis: {
      type: 'value',
      axisLabel: { ...AXIS_LABEL, formatter: (v: number) => (v >= 1000 ? `${v / 1000}k` : v) },
      splitLine: SPLIT_LINE,
    },
    yAxis: {
      type: 'category',
      data: sorted.map(s => s.media_source),
      axisLabel: { ...AXIS_LABEL, fontSize: 10, width: 130, overflow: 'truncate' },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [{
      type: 'bar',
      data: sorted.map(s => ({
        value: s.article_count,
        tone: s.avg_tone,
        itemStyle: { color: toneColor(s.avg_tone), opacity: 0.8, borderRadius: [0, 4, 4, 0] },
      })),
      barMaxWidth: 16,
      label: {
        show: true, position: 'right', color: '#a1a1aa', fontSize: 9,
        fontFamily: 'JetBrains Mono, monospace',
        formatter: (p: SourceBarParam) => `tone ${p.data.tone.toFixed(1)}`,
      },
    }],
  };
}

// --- Local controls ---

function DateInput({ label, value, min, max, onChange }: {
  label: string;
  value: string;
  min?: string;
  max?: string;
  onChange: (v: string) => void;
}) {
  return (
    <div>
      <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">{label}</label>
      <input
        type="date"
        value={value}
        min={min}
        max={max}
        onChange={e => e.target.value && onChange(e.target.value)}
        className="text-xs text-zinc-400 rounded-lg px-3 py-2 outline-none border border-zinc-800/50 transition-colors focus:border-cyan-500/30 [color-scheme:dark]"
        style={{ background: '#09090b' }}
      />
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

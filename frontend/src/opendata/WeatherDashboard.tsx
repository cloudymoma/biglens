import { useState, useEffect, useMemo } from 'react';
import ReactECharts from 'echarts-for-react';
import * as echarts from 'echarts';
import { RadioTower, Flame, Snowflake, CloudRain, CloudSnow } from 'lucide-react';
import type { WeatherDashboardData, WeatherStation, WeatherDaily } from '../types';
import { fetchWeatherMeta, fetchWeatherDashboard } from '../api';
import { MetricCard, EmptyState, ErrorBanner } from '../dashboards/shared';

// Mirrors the backend caps: 7-31 day trailing window, snapshots since 1900.
const MIN_DATE = '1900-01-01';
const WINDOWS = [7, 14, 30];

// Reporting is provisional for the trailing days: GHCN stations backfill
// over about a week, so the newest 1-3 days always undercount.
const PROVISIONAL_DAYS = 3;

const CHART_TOOLTIP = {
  backgroundColor: 'rgba(17,17,20,0.95)',
  borderColor: '#27272a',
  textStyle: { color: '#e4e4e7', fontSize: 12 },
};

const AXIS_LABEL = { color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' };
const SPLIT_LINE = { lineStyle: { color: '#1f1f23', type: 'dashed' } };

// Diverging temperature ramp, deep cold blue → neutral → alarm red.
const TEMP_COLORS = ['#312e81', '#2563eb', '#22d3ee', '#e4e4e7', '#fbbf24', '#f97316', '#dc2626'];
// Sequential precipitation ramp, light teal → indigo.
const PRCP_COLORS = ['#99f6e4', '#14b8a6', '#0e7490', '#4338ca'];

type MapMode = 'temp' | 'prcp';

const fmt1 = (v: number | null) => (v === null ? '—' : v.toFixed(1));

export default function WeatherDashboard() {
  const [latestDate, setLatestDate] = useState('');
  const [metaError, setMetaError] = useState('');

  const [date, setDate] = useState('');
  const [days, setDays] = useState(30);
  const [mode, setMode] = useState<MapMode>('temp');

  const [data, setData] = useState<WeatherDashboardData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const [mapReady, setMapReady] = useState(false);

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

  // latest_date bounds the picker; default_date (the freshest day with
  // settled coverage) is what loads first — the newest 1-2 days are nearly
  // empty while stations backfill.
  useEffect(() => {
    fetchWeatherMeta()
      .then(meta => {
        setLatestDate(meta.latest_date);
        setDate(meta.default_date);
      })
      .catch(e => setMetaError(e.response?.data || e.message));
  }, []);

  useEffect(() => {
    if (!date) return;
    setLoading(true);
    setError('');
    fetchWeatherDashboard(date, days)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [date, days]);

  const leaderboards = useMemo(() => {
    const stations = data?.stations ?? [];
    const byTmax = stations.filter(s => s.tmax_c !== null);
    const byTmin = stations.filter(s => s.tmin_c !== null);
    const byPrcp = stations.filter(s => (s.prcp_mm ?? 0) > 0);
    return {
      hottest: [...byTmax].sort((a, b) => b.tmax_c! - a.tmax_c!).slice(0, 10),
      coldest: [...byTmin].sort((a, b) => a.tmin_c! - b.tmin_c!).slice(0, 10),
      wettest: [...byPrcp].sort((a, b) => b.prcp_mm! - a.prcp_mm!).slice(0, 10),
    };
  }, [data]);

  // Provisional shading only applies when the window ends near the bleeding
  // edge of the dataset, where backfill is still filling days in.
  const provisional = !!latestDate && !!date
    && Date.parse(latestDate) - Date.parse(date) < PROVISIONAL_DAYS * 86400000;

  return (
    <div className="space-y-6">
      {/* Filter bar: snapshot day, trailing window, map metric */}
      <div className="rounded-2xl border border-zinc-800/50 p-4 flex flex-wrap items-end gap-4" style={{ background: '#111114' }}>
        <div>
          <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">Snapshot Day</label>
          <input
            type="date"
            value={date}
            min={MIN_DATE}
            max={latestDate}
            onChange={e => e.target.value && setDate(e.target.value)}
            className="text-xs text-zinc-400 rounded-lg px-3 py-2 outline-none border border-zinc-800/50 transition-colors focus:border-cyan-500/30 [color-scheme:dark]"
            style={{ background: '#09090b' }}
          />
        </div>
        <div>
          <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">Trend Window</label>
          <div className="flex gap-1.5">
            {WINDOWS.map(w => (
              <ToggleChip key={w} label={`${w} days`} active={days === w} onClick={() => setDays(w)} />
            ))}
          </div>
        </div>
        <div>
          <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">Map Metric</label>
          <div className="flex gap-1.5">
            <ToggleChip label="Temperature" active={mode === 'temp'} onClick={() => setMode('temp')} />
            <ToggleChip label="Precipitation" active={mode === 'prcp'} onClick={() => setMode('prcp')} />
          </div>
        </div>
        <p className="text-[10px] text-zinc-600 ml-auto self-center max-w-[280px] leading-relaxed">
          NOAA GHCN-Daily · latest observation {latestDate || '…'} · defaults to the last day with
          settled coverage; newer days are still backfilling. Units: °C / mm.
        </p>
      </div>

      {metaError && <ErrorBanner message={metaError} />}
      {error && <ErrorBanner message={error} />}
      {!metaError && !error && (loading || !data) && <LoadingPulse />}

      {!metaError && !error && !loading && data && (
        <>
          {/* KPI row (rolled up server-side) */}
          <div className="grid grid-cols-1 md:grid-cols-3 xl:grid-cols-5 gap-4">
            <MetricCard label="Stations Reporting" value={data.overall.stations_reporting.toLocaleString()}
              icon={<RadioTower size={18} />} detail={data.snapshot_date} accentColor="#38bdf8" />
            <MetricCard label="Hottest" value={data.overall.hottest ? `${data.overall.hottest.value.toFixed(1)}°C` : '—'}
              icon={<Flame size={18} />} detail={extremeDetail(data.overall.hottest)} accentColor="#ef4444" />
            <MetricCard label="Coldest" value={data.overall.coldest ? `${data.overall.coldest.value.toFixed(1)}°C` : '—'}
              icon={<Snowflake size={18} />} detail={extremeDetail(data.overall.coldest)} accentColor="#60a5fa" />
            <MetricCard label="Wettest" value={data.overall.wettest ? `${data.overall.wettest.value.toFixed(1)} mm` : '—'}
              icon={<CloudRain size={18} />} detail={extremeDetail(data.overall.wettest)} accentColor="#2dd4bf" />
            <MetricCard label="Snow Reports" value={data.overall.snow_stations.toLocaleString()}
              icon={<CloudSnow size={18} />} detail="Stations with snowfall" accentColor="#a78bfa" />
          </div>

          {/* Panel 1: the world map */}
          <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
            <h3 className="text-sm font-semibold text-white mb-1">
              {mode === 'temp' ? 'Daily High Temperature' : 'Daily Precipitation'}
            </h3>
            <p className="text-xs text-zinc-500 mb-4">
              {mode === 'temp'
                ? 'One dot per station, colored by TMAX'
                : 'Stations with rainfall · bubble size & color = mm'}
              {' '}· {data.snapshot_date} · drag to pan, scroll to zoom
            </p>
            {mapReady && data.stations.length > 0 ? (
              <div className="h-[480px]">
                {/* key remounts the chart per metric so no canvas state can
                    leak between the two series shapes */}
                <ReactECharts
                  key={mode}
                  option={mode === 'temp' ? tempMapOption(data.stations) : prcpMapOption(data.stations)}
                  style={{ height: '100%' }}
                  notMerge
                />
              </div>
            ) : mapReady ? (
              <EmptyState text="No station observations on this day" />
            ) : (
              <div className="h-[480px] rounded-xl animate-pulse" style={{ background: '#09090b' }} />
            )}
          </div>

          {/* Panel 2: trend + reporting coverage */}
          <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
            <div className="lg:col-span-2 rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
              <h3 className="text-sm font-semibold text-white mb-1">Temperature &amp; Precipitation Trend</h3>
              <p className="text-xs text-zinc-500 mb-2">
                Mean across reporting stations (a network mean, not a global temperature)
                {provisional && ' · shaded tail = provisional'}
              </p>
              {data.daily.length > 0 ? (
                <div className="h-[300px]">
                  <ReactECharts option={trendOption(data.daily, provisional)} style={{ height: '100%' }} notMerge />
                </div>
              ) : (
                <EmptyState text="No daily data in this window" />
              )}
            </div>
            <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
              <h3 className="text-sm font-semibold text-white mb-1">Reporting Coverage</h3>
              <p className="text-xs text-zinc-500 mb-2">Stations per day, by element</p>
              {data.daily.length > 0 ? (
                <div className="h-[300px]">
                  <ReactECharts option={coverageOption(data.daily)} style={{ height: '100%' }} notMerge />
                </div>
              ) : (
                <EmptyState text="No daily data" />
              )}
            </div>
          </div>

          {/* Panel 3: extremes leaderboards */}
          <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
            <Leaderboard title="Hottest Stations" unit="°C" accent="#ef4444"
              rows={leaderboards.hottest.map(s => ({ station: s, value: s.tmax_c! }))} />
            <Leaderboard title="Coldest Stations" unit="°C" accent="#60a5fa"
              rows={leaderboards.coldest.map(s => ({ station: s, value: s.tmin_c! }))} />
            <Leaderboard title="Wettest Stations" unit="mm" accent="#2dd4bf"
              rows={leaderboards.wettest.map(s => ({ station: s, value: s.prcp_mm! }))} />
          </div>
        </>
      )}
    </div>
  );
}

function extremeDetail(e: { station: string; country_state: string } | null): string {
  return e ? `${e.station} · ${e.country_state}` : 'No reports';
}

function place(s: WeatherStation): string {
  return s.state ? `${s.state}, ${s.country}` : s.country;
}

// --- Leaderboard ---

function Leaderboard({ title, unit, accent, rows }: {
  title: string;
  unit: string;
  accent: string;
  rows: { station: WeatherStation; value: number }[];
}) {
  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <h3 className="text-sm font-semibold text-white mb-1">{title}</h3>
      <p className="text-xs text-zinc-500 mb-4">Top {rows.length || 10} on the snapshot day</p>
      {rows.length > 0 ? (
        <table className="w-full text-xs">
          <tbody>
            {rows.map((r, i) => (
              <tr key={`${r.station.name}-${i}`} className="border-t border-zinc-800/40 text-zinc-400">
                <td className="py-1.5 pr-2 font-mono text-[10px] text-zinc-600 w-6">{i + 1}</td>
                <td className="py-1.5 pr-3">
                  <span className="text-zinc-300 block truncate max-w-[180px]" title={r.station.name}>{r.station.name}</span>
                  <span className="text-[10px] text-zinc-600 font-mono">{place(r.station)}</span>
                </td>
                <td className="py-1.5 text-right font-mono whitespace-nowrap" style={{ color: accent }}>
                  {r.value.toFixed(1)} {unit}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <EmptyState text="No qualifying stations" />
      )}
    </div>
  );
}

// --- Chart options ---

// A fresh geo config per option build: sharing one object between chart
// instances risks ECharts-internal mutation leaking across renders.
function geoBase() {
  return {
    map: 'world',
    roam: true,
    zoom: 1.2,
    itemStyle: { areaColor: '#18181b', borderColor: '#27272a' },
    emphasis: { itemStyle: { areaColor: '#1f1f23' }, label: { show: false } },
    select: { disabled: true },
  };
}

// Station scatter rows are strictly numeric [lon, lat, metric]: mixed
// string/null trailing dimensions broke geo roam in ECharts 6 (points kept
// their pixel positions while the map zoomed underneath). Station details
// come from a dataIndex lookup into the filtered array instead.
interface StationScatterParam { value: [number, number, number]; dataIndex: number; }

function stationTooltip(s: WeatherStation): string {
  return `<div style="color:#f4f4f5;font-size:12px;font-weight:600">${s.name}</div>
     <div style="color:#a1a1aa;font-size:11px">${place(s)}</div>
     <div style="color:#e4e4e7;font-size:11px">high ${fmt1(s.tmax_c)}°C · low ${fmt1(s.tmin_c)}°C · precip ${fmt1(s.prcp_mm)} mm</div>`;
}

function tempMapOption(stations: WeatherStation[]) {
  const pts = stations.filter(s => s.tmax_c !== null);
  return {
    backgroundColor: 'transparent',
    tooltip: {
      ...CHART_TOOLTIP,
      formatter: (p: StationScatterParam) => stationTooltip(pts[p.dataIndex]),
    },
    visualMap: {
      min: -40,
      max: 45,
      dimension: 2,
      orient: 'horizontal',
      left: 'center',
      bottom: 0,
      itemWidth: 10,
      itemHeight: 90,
      text: ['Hot', 'Cold'],
      textStyle: { color: '#71717a', fontSize: 9 },
      inRange: { color: TEMP_COLORS },
    },
    geo: geoBase(),
    series: [{
      type: 'scatter',
      coordinateSystem: 'geo',
      // progressive 0 disables incremental rendering: above ~3k points
      // ECharts renders scatter progressively, and that layer is not
      // re-projected on geo roam — dots freeze while the map pans/zooms
      // underneath (same reason large:true is avoided).
      progressive: 0,
      symbolSize: 4,
      data: pts.map(s => [s.longitude, s.latitude, s.tmax_c]),
      itemStyle: { opacity: 0.8 },
    }],
  };
}

function prcpMapOption(stations: WeatherStation[]) {
  const pts = stations.filter(s => (s.prcp_mm ?? 0) > 0);
  const maxPrcp = Math.max(...pts.map(s => s.prcp_mm!), 1);
  return {
    backgroundColor: 'transparent',
    tooltip: {
      ...CHART_TOOLTIP,
      formatter: (p: StationScatterParam) => stationTooltip(pts[p.dataIndex]),
    },
    visualMap: {
      min: 0,
      max: maxPrcp,
      dimension: 2,
      orient: 'horizontal',
      left: 'center',
      bottom: 0,
      itemWidth: 10,
      itemHeight: 90,
      text: ['Heavy', 'Light'],
      textStyle: { color: '#71717a', fontSize: 9 },
      inRange: { color: PRCP_COLORS },
    },
    geo: geoBase(),
    series: [{
      type: 'scatter',
      coordinateSystem: 'geo',
      // See tempMapOption: progressive rendering breaks geo roam tracking.
      progressive: 0,
      data: pts.map(s => [s.longitude, s.latitude, s.prcp_mm]),
      symbolSize: (v: number[]) => 3 + 18 * Math.sqrt(v[2] / maxPrcp),
      itemStyle: { opacity: 0.7 },
    }],
  };
}

function trendOption(daily: WeatherDaily[], provisional: boolean) {
  const markArea = provisional && daily.length > PROVISIONAL_DAYS
    ? {
        silent: true,
        itemStyle: { color: 'rgba(113,113,122,0.08)' },
        data: [[{ xAxis: daily[daily.length - PROVISIONAL_DAYS].date.slice(5) }, { xAxis: daily[daily.length - 1].date.slice(5) }]],
      }
    : undefined;
  return {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
    legend: { top: 0, textStyle: { color: '#a1a1aa', fontSize: 10 }, icon: 'circle', itemWidth: 8, itemHeight: 8 },
    grid: { left: 8, right: 8, bottom: 8, top: 30, containLabel: true },
    xAxis: {
      type: 'category',
      data: daily.map(d => d.date.slice(5)),
      axisLabel: { ...AXIS_LABEL, fontSize: 9 },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    yAxis: [
      { type: 'value', name: '°C', nameTextStyle: AXIS_LABEL, axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE },
      { type: 'value', name: 'mm', nameTextStyle: AXIS_LABEL, axisLabel: AXIS_LABEL, splitLine: { show: false } },
    ],
    series: [
      {
        name: 'Mean precip',
        type: 'bar',
        yAxisIndex: 1,
        data: daily.map(d => d.avg_prcp_mm),
        barMaxWidth: 14,
        itemStyle: { color: 'rgba(13,148,136,0.45)', borderRadius: [3, 3, 0, 0] },
      },
      {
        name: 'Mean high',
        type: 'line',
        data: daily.map(d => d.avg_tmax_c),
        smooth: true,
        showSymbol: false,
        lineStyle: { color: '#f97316', width: 2 },
        itemStyle: { color: '#f97316' },
        markArea,
      },
      {
        name: 'Mean low',
        type: 'line',
        data: daily.map(d => d.avg_tmin_c),
        smooth: true,
        showSymbol: false,
        lineStyle: { color: '#60a5fa', width: 2 },
        itemStyle: { color: '#60a5fa' },
      },
    ],
  };
}

function coverageOption(daily: WeatherDaily[]) {
  return {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
    legend: { top: 0, textStyle: { color: '#a1a1aa', fontSize: 10 }, icon: 'circle', itemWidth: 8, itemHeight: 8 },
    grid: { left: 8, right: 8, bottom: 8, top: 30, containLabel: true },
    xAxis: {
      type: 'category',
      data: daily.map(d => d.date.slice(5)),
      axisLabel: { ...AXIS_LABEL, fontSize: 9 },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    yAxis: {
      type: 'value',
      axisLabel: { ...AXIS_LABEL, formatter: (v: number) => (v >= 1000 ? `${v / 1000}k` : v) },
      splitLine: SPLIT_LINE,
    },
    series: [
      {
        name: 'Precip stations',
        type: 'bar',
        data: daily.map(d => d.prcp_stations),
        barMaxWidth: 10,
        itemStyle: { color: 'rgba(13,148,136,0.6)', borderRadius: [2, 2, 0, 0] },
      },
      {
        name: 'Temp stations',
        type: 'bar',
        data: daily.map(d => d.tmax_stations),
        barMaxWidth: 10,
        itemStyle: { color: 'rgba(2,132,199,0.6)', borderRadius: [2, 2, 0, 0] },
      },
    ],
  };
}

// --- Local controls ---

function ToggleChip({ label, active, onClick }: { label: string; active: boolean; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className={`text-xs rounded-lg px-3 py-2 border cursor-pointer transition-colors ${
        active
          ? 'border-cyan-500/30 bg-cyan-500/5 text-cyan-400'
          : 'border-zinc-800/50 text-zinc-400 hover:border-zinc-700/60'
      }`}
      style={{ background: active ? undefined : '#09090b' }}
    >
      {label}
    </button>
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

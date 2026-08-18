import React, { useState, useEffect, useMemo } from 'react';
import ReactECharts from 'echarts-for-react';
import {
  ChevronDown, Flame, Megaphone, CalendarDays, Download, MapPin, Zap,
} from 'lucide-react';
import type { SemMarket, SemMeta, SemMatrixRow, SemSafetyRow } from '../../types';
import { fetchSemMeta, fetchSemDashboard, fetchSemSafety } from '../../api';
import { MetricCard, EmptyState, ErrorBanner } from '../../dashboards/shared';
import { CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE, LoadingPulse } from './shared';
import GeoPanel from './GeoPanel';
import PulsePanel from './PulsePanel';
import TermPanel from './TermPanel';
import SafetyPanel, { computeSafetyStatus } from './SafetyPanel';
import type { SafetyLevel } from './SafetyPanel';

// SEM Insights dashboard: breakout keyword matrix (W1), geo bid modifiers
// (W2), opportunity table with Google Ads Editor CSV exports (W4), US hourly
// pulse (W5) and term drill-down (W6), driven by the market / geo / snapshot /
// velocity controls. See sem_dashboard_design.md.

// volume_rank 0 = not charting in the top 25; plotted as its own band left of
// rank 25 on the inverted x-axis.
const UNRANKED_X = 26;

// Velocity slider maps 0..100 to a geometric +50%..+5000% threshold (0 = off):
// percent gains span two orders of magnitude, so a linear slider would cram
// everything interesting into its first tenth.
const GAIN_MIN = 50;
const GAIN_MAX = 5000;
function sliderToGain(t: number): number {
  if (t <= 0) return 0;
  return Math.round(GAIN_MIN * Math.pow(GAIN_MAX / GAIN_MIN, t / 100));
}

interface MatrixDatum {
  value: [number, number];
  row: SemMatrixRow;
}

export default function SemDashboard() {
  const [market, setMarket] = useState<SemMarket>('us');
  const [meta, setMeta] = useState<SemMeta | null>(null);
  const [metaError, setMetaError] = useState('');

  const [refreshDate, setRefreshDate] = useState('');
  const [geo, setGeo] = useState(''); // country code (global) or DMA name (us; '' = national)

  const [rows, setRows] = useState<SemMatrixRow[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  const [velocity, setVelocity] = useState(0); // slider position 0..100
  const [checked, setChecked] = useState<Record<string, boolean>>({});

  // W2 follows the selected term (defaults to the top gainer); W6 opens only
  // on an explicit term click from W1/W4/W5.
  const [selectedTerm, setSelectedTerm] = useState('');
  const [drillOpen, setDrillOpen] = useState(false);

  // W3 market-level news context; the overlay toggle applies it to W1/W4.
  const [safetyRows, setSafetyRows] = useState<SemSafetyRow[]>([]);
  const [safetyError, setSafetyError] = useState('');
  const [safetyLoading, setSafetyLoading] = useState(true);
  const [overlayOn, setOverlayOn] = useState(true);

  const gainThreshold = sliderToGain(velocity);

  useEffect(() => {
    setMeta(null);
    setMetaError('');
    setGeo('');
    fetchSemMeta(market)
      .then(m => {
        setMeta(m);
        setRefreshDate(m.latest_refresh_date);
        if (market === 'global') {
          const codes = m.countries.map(c => c.code);
          const preferred = ['US', 'GB', 'JP'].find(c => codes.includes(c));
          setGeo(preferred || codes[0] || '');
        }
      })
      .catch(e => setMetaError(e.response?.data || e.message));
  }, [market]);

  useEffect(() => {
    if (!refreshDate || (market === 'global' && !geo)) return;
    setLoading(true);
    setError('');
    setChecked({});
    fetchSemDashboard(market, refreshDate, geo)
      .then(d => {
        setRows(d.matrix);
        setSelectedTerm(d.matrix[0]?.term ?? '');
        setDrillOpen(false);
      })
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [market, refreshDate, geo]);

  function selectTerm(term: string) {
    setSelectedTerm(term);
    setDrillOpen(true);
  }

  useEffect(() => {
    if (market === 'global' && !geo) return;
    setSafetyLoading(true);
    setSafetyError('');
    fetchSemSafety(market, geo)
      .then(d => setSafetyRows(d.rows))
      .catch(e => setSafetyError(e.response?.data || e.message))
      .finally(() => setSafetyLoading(false));
  }, [market, geo]);

  const safetyStatus = useMemo(() => computeSafetyStatus(safetyRows), [safetyRows]);
  // The overlay tint only fires on a non-green market with the toggle on.
  const overlayLevel: SafetyLevel | null =
    overlayOn && safetyStatus && safetyStatus.level !== 'green' ? safetyStatus.level : null;

  const filtered = useMemo(
    () => rows.filter(r => r.percent_gain >= gainThreshold),
    [rows, gainThreshold],
  );
  const unrankedCount = filtered.filter(r => r.volume_rank === 0).length;
  const topGainer = filtered[0];

  const geoLabel = market === 'us'
    ? (geo || 'United States (national)')
    : (meta?.countries.find(c => c.code === geo)?.name || geo);

  function toggleChecked(term: string) {
    setChecked(prev => ({ ...prev, [term]: !prev[term] }));
  }

  const checkedRows = filtered.filter(r => checked[r.term]);

  function exportKeywords() {
    const campaign = `SEM-Trends-${refreshDate}`;
    downloadCsv(`sem-keywords-${refreshDate}.csv`, [
      ['Campaign', 'Ad group', 'Keyword', 'Match type', 'Max CPC'],
      ...filtered.map(r => [campaign, geoLabel, r.term, 'Phrase', '']),
    ]);
  }

  function exportNegatives() {
    const campaign = `SEM-Trends-${refreshDate}`;
    downloadCsv(`sem-negatives-${refreshDate}.csv`, [
      ['Campaign', 'Keyword', 'Match type'],
      ...checkedRows.map(r => [campaign, r.term, 'Phrase']),
    ]);
  }

  if (metaError) return <ErrorBanner message={metaError} />;
  if (!meta) return <LoadingPulse />;

  return (
    <div className="space-y-6">
      {/* Controls: market toggle + geo + snapshot + velocity */}
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

        <GeoCombobox
          label={market === 'us' ? 'Metro Area (DMA)' : 'Country'}
          options={market === 'us'
            ? [{ name: 'All US (national)', code: '' }, ...meta.dmas.map(d => ({ name: d.name, code: d.name }))]
            : meta.countries.map(c => ({ name: c.name, code: c.code }))}
          value={geo}
          onChange={setGeo}
        />

        <div>
          <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">Snapshot</label>
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

        <div>
          <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">
            Velocity ≥ {gainThreshold > 0 ? `+${gainThreshold.toLocaleString()}%` : 'any'}
          </label>
          <input
            type="range"
            min={0}
            max={100}
            value={velocity}
            onChange={e => setVelocity(Number(e.target.value))}
            className="w-40 accent-cyan-500 cursor-pointer"
            style={{ marginBottom: 6 }}
          />
        </div>

        <label className="flex items-center gap-2 text-xs text-zinc-400 cursor-pointer select-none pb-2.5">
          <input
            type="checkbox"
            checked={overlayOn}
            onChange={e => setOverlayOn(e.target.checked)}
            className="accent-amber-500 cursor-pointer"
          />
          Brand-safety overlay
        </label>

        <p className="text-[10px] text-zinc-600 ml-auto self-center max-w-[240px] leading-relaxed">
          Rising searches joined against the top-25 chart of the same snapshot. Unranked + high velocity = bid before CPCs catch up.
        </p>
      </div>

      {error && <ErrorBanner message={error} />}
      {loading && <LoadingPulse />}

      {!loading && !error && (
        <>
          {/* Summary metrics */}
          <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
            <MetricCard label="Top Breakout" value={topGainer ? `+${topGainer.percent_gain.toLocaleString()}%` : '—'}
              icon={<Flame size={18} />} detail={topGainer?.term || 'No rising terms'} accentColor="#fbbf24" />
            <MetricCard label="Arbitrage Keywords" value={String(unrankedCount)} icon={<Zap size={18} />}
              detail="Rising but not yet in the top 25" accentColor="#38bdf8" />
            <MetricCard label="Rising Terms" value={String(filtered.length)} icon={<Megaphone size={18} />}
              detail={`In ${geoLabel}`} accentColor="#34d399" />
            <MetricCard label="Snapshot" value={refreshDate} icon={<CalendarDays size={18} />}
              detail="Daily refresh · lags 1–2 days" accentColor="#a78bfa" />
          </div>

          {/* W1: Breakout Keyword Matrix · W2: Geo Bid Modifiers */}
          <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
            <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
              <h3 className="text-sm font-semibold text-white mb-1">Breakout Keyword Matrix</h3>
              <p className="text-xs text-zinc-500 mb-4">
                Momentum (week-over-week gain, log scale) vs mainstream volume rank in {geoLabel}.
                Left = not charting yet · bubble size = search score · amber = arbitrage zone.
                Click a bubble to drill down.
              </p>
              {filtered.length > 0 ? (
                <div className="h-[400px]">
                  <ReactECharts
                    option={matrixOption(filtered, overlayLevel)}
                    style={{ height: '100%' }}
                    notMerge
                    onEvents={{
                      click: (params: { data?: MatrixDatum }) => {
                        if (params.data?.row) selectTerm(params.data.row.term);
                      },
                    }}
                  />
                </div>
              ) : (
                <EmptyState text="No rising terms match the current velocity filter" />
              )}
            </div>

            {selectedTerm ? (
              <GeoPanel market={market} refreshDate={refreshDate} geo={geo} term={selectedTerm} />
            ) : (
              <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
                <EmptyState text="Select a term to see geo demand and bid modifiers" />
              </div>
            )}
          </div>

          {/* W6: Term drill-down (opens on term click) */}
          {drillOpen && selectedTerm && (
            <TermPanel
              market={market}
              refreshDate={refreshDate}
              geo={geo}
              term={selectedTerm}
              onClose={() => setDrillOpen(false)}
            />
          )}

          {/* W3: Brand safety radar · W4: Opportunity table + exports */}
          <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
          <SafetyPanel
            rows={safetyRows}
            status={safetyStatus}
            loading={safetyLoading}
            error={safetyError}
            marketLabel={market === 'us' ? 'the United States' : geoLabel}
          />
          <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
            <div className="flex items-center justify-between flex-wrap gap-3 mb-1">
              <h3 className="text-sm font-semibold text-white flex items-center gap-2">
                Keyword Opportunities
                {overlayLevel && (
                  <span className={`text-[10px] font-semibold px-1.5 py-0.5 rounded border ${
                    overlayLevel === 'red'
                      ? 'text-rose-400 border-rose-500/30 bg-rose-500/5'
                      : 'text-amber-400 border-amber-500/30 bg-amber-500/5'
                  }`}>
                    {overlayLevel === 'red' ? '🔴 negative news cycle — review before bidding' : '🟡 tone caution'}
                  </span>
                )}
              </h3>
              <div className="flex gap-2">
                <button
                  onClick={exportKeywords}
                  disabled={filtered.length === 0}
                  className="flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-lg border border-cyan-500/30 text-cyan-400 hover:bg-cyan-500/10 transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
                >
                  <Download size={12} /> Keywords CSV ({filtered.length})
                </button>
                <button
                  onClick={exportNegatives}
                  disabled={checkedRows.length === 0}
                  className="flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-lg border border-rose-500/30 text-rose-400 hover:bg-rose-500/10 transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
                >
                  <Download size={12} /> Negatives CSV ({checkedRows.length})
                </button>
              </div>
            </div>
            <p className="text-xs text-zinc-500 mb-4">
              Google Ads Editor import format. Check rows to build the negative-keyword list.
            </p>
            {filtered.length > 0 ? (
              <div className="overflow-x-auto max-h-[480px] overflow-y-auto">
                <table className="w-full text-xs">
                  <thead className="sticky top-0" style={{ background: '#111114' }}>
                    <tr className="text-left text-[10px] font-mono uppercase text-zinc-600 border-b border-zinc-800/60">
                      <th className="py-2 pr-2 w-8"></th>
                      <th className="py-2 pr-4">Term</th>
                      <th className="py-2 pr-4 text-right">Gain</th>
                      <th className="py-2 pr-4 text-right">Volume Rank</th>
                      <th className="py-2 pr-4 text-right">Score</th>
                      <th className="py-2 pr-4 text-right">
                        <span className="inline-flex items-center gap-1"><MapPin size={9} />{market === 'us' ? 'DMAs' : 'Regions'}</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {filtered.map(r => (
                      <tr key={r.term} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                        <td className="py-1.5 pr-2">
                          <input
                            type="checkbox"
                            checked={!!checked[r.term]}
                            onChange={() => toggleChecked(r.term)}
                            className="accent-rose-500 cursor-pointer"
                            title="Add to negatives export"
                          />
                        </td>
                        <td className="py-1.5 pr-4">
                          <button
                            onClick={() => selectTerm(r.term)}
                            className="text-zinc-300 hover:text-cyan-400 transition-colors cursor-pointer text-left"
                            title="Drill down"
                          >
                            {r.term}
                          </button>
                        </td>
                        <td className="py-1.5 pr-4 text-right font-mono text-amber-400">+{r.percent_gain.toLocaleString()}%</td>
                        <td className="py-1.5 pr-4 text-right font-mono">
                          {r.volume_rank > 0 ? (
                            <span className="text-zinc-400">#{r.volume_rank}</span>
                          ) : (
                            <span className="text-[10px] font-semibold px-1.5 py-0.5 rounded border text-cyan-400 border-cyan-500/20 bg-cyan-500/5">UNRANKED</span>
                          )}
                        </td>
                        <td className="py-1.5 pr-4 text-right font-mono text-zinc-500">
                          {r.score > 0 ? r.score : <span className="text-violet-400 text-[10px]">new</span>}
                        </td>
                        <td className="py-1.5 pr-4 text-right font-mono text-zinc-500">{r.geo_spread}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <EmptyState text="No rising terms match the current velocity filter" />
            )}
          </div>
          </div>

          {/* W5: US real-time hourly pulse */}
          {market === 'us' && <PulsePanel onSelectTerm={selectTerm} />}
        </>
      )}
    </div>
  );
}

// --- W1 chart option ---

function matrixOption(rows: SemMatrixRow[], overlayLevel: SafetyLevel | null) {
  const ranked: MatrixDatum[] = [];
  const unranked: MatrixDatum[] = [];
  for (const r of rows) {
    const datum: MatrixDatum = {
      // log axis cannot plot 0; rising gains are positive but clamp defensively.
      value: [r.volume_rank > 0 ? r.volume_rank : UNRANKED_X, Math.max(r.percent_gain, 1)],
      row: r,
    };
    (r.volume_rank > 0 ? ranked : unranked).push(datum);
  }

  const symbolSize = (_: unknown, params: { data: MatrixDatum }) =>
    10 + params.data.row.score * 0.3;

  // Market-level brand-safety tint: the news signal is country-grained, so
  // every bubble carries the same border warning color.
  const overlayBorder = overlayLevel === 'red' ? '#f43f5e' : overlayLevel === 'amber' ? '#f59e0b' : null;

  const tooltipFormatter = (params: { data: MatrixDatum }) => {
    const r = params.data.row;
    return `<div style="font-weight:600;color:#f4f4f5;font-size:12px;margin-bottom:4px">${r.term}</div>
      <div style="color:#fbbf24">+${r.percent_gain.toLocaleString()}% week-over-week</div>
      <div style="color:#a1a1aa">Volume rank: ${r.volume_rank > 0 ? `#${r.volume_rank}` : 'not in top 25'}</div>
      <div style="color:#a1a1aa">Score: ${r.score > 0 ? `${r.score}/100` : 'too new to score'}</div>
      <div style="color:#a1a1aa">Rising in ${r.geo_spread} geo${r.geo_spread === 1 ? '' : 's'}</div>`;
  };

  return {
    backgroundColor: 'transparent',
    tooltip: { ...CHART_TOOLTIP, formatter: tooltipFormatter },
    grid: { left: 48, right: 24, bottom: 40, top: 24 },
    xAxis: {
      type: 'value',
      inverse: true, // Unranked band (26) and weak ranks left, rank #1 right
      min: 0,
      max: UNRANKED_X + 1,
      interval: 1,
      axisLabel: {
        ...AXIS_LABEL,
        formatter: (v: number) => {
          if (v === UNRANKED_X) return 'Unranked';
          if ([1, 5, 10, 15, 20, 25].includes(v)) return `#${v}`;
          return '';
        },
      },
      splitLine: { show: false },
      name: 'Mainstream volume rank →',
      nameLocation: 'end',
      nameTextStyle: { color: '#52525b', fontSize: 10 },
    },
    yAxis: {
      type: 'log',
      logBase: 10,
      axisLabel: { ...AXIS_LABEL, formatter: (v: number) => `+${v >= 1000 ? `${v / 1000}k` : v}%` },
      splitLine: SPLIT_LINE,
      name: 'Velocity',
      nameTextStyle: { color: '#52525b', fontSize: 10 },
    },
    series: [
      {
        name: 'Arbitrage (unranked)',
        type: 'scatter',
        data: unranked,
        symbolSize,
        itemStyle: {
          color: 'rgba(251,191,36,0.75)',
          borderColor: overlayBorder ?? '#f59e0b',
          borderWidth: overlayBorder ? 2 : 1,
        },
        markArea: {
          silent: true,
          itemStyle: { color: 'rgba(251,191,36,0.04)' },
          label: { color: '#a16207', fontSize: 10, position: 'insideTop' },
          data: [[
            { name: 'Arbitrage zone — bid before CPCs catch up', xAxis: UNRANKED_X + 1 },
            { xAxis: 20 },
          ]],
        },
      },
      {
        name: 'Charting (top 25)',
        type: 'scatter',
        data: ranked,
        symbolSize,
        itemStyle: {
          color: 'rgba(56,189,248,0.7)',
          borderColor: overlayBorder ?? '#0284c7',
          borderWidth: overlayBorder ? 2 : 1,
        },
      },
    ],
  };
}

// --- CSV export (client-side, Google Ads Editor import format) ---

function csvEscape(v: string): string {
  return /[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v;
}

function downloadCsv(filename: string, rows: string[][]) {
  const body = rows.map(r => r.map(csvEscape).join(',')).join('\r\n');
  const blob = new Blob([body], { type: 'text/csv;charset=utf-8;' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

// --- Geo combobox (searchable; countries or DMAs) ---

function GeoCombobox({ label, options, value, onChange }: {
  label: string;
  options: { name: string; code: string }[];
  value: string;
  onChange: (code: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const ref = React.useRef<HTMLDivElement>(null);

  const selected = options.find(o => o.code === value);
  const filtered = query
    ? options.filter(o => o.name.toLowerCase().includes(query.toLowerCase()))
    : options;

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
        style={{ background: '#09090b', minWidth: 200 }}
      >
        {selected ? selected.name : 'Select…'}
        <ChevronDown size={12} className={`absolute right-2.5 top-1/2 -translate-y-1/2 text-zinc-600 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>

      {open && (
        <div className="absolute z-50 mt-1 w-72 rounded-lg border border-zinc-800/60 shadow-xl overflow-hidden" style={{ background: '#111114' }}>
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
            {filtered.length > 0 ? filtered.map(o => (
              <button
                key={o.code || o.name}
                onClick={() => { onChange(o.code); setOpen(false); setQuery(''); }}
                className={`w-full text-left px-3 py-1.5 text-xs cursor-pointer transition-colors ${
                  o.code === value ? 'text-cyan-400 bg-cyan-500/5' : 'text-zinc-400 hover:bg-zinc-800/50 hover:text-zinc-200'
                }`}
              >
                {o.name}
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


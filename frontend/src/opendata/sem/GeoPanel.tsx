import { useState, useEffect, useMemo } from 'react';
import ReactECharts from 'echarts-for-react';
import { format } from 'echarts';
import { MapPin } from 'lucide-react';
import type { SemMarket, SemGeoRow } from '../../types';
import { fetchSemGeo } from '../../api';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import { CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE } from './shared';

// W2 — Geo Interest for the selected term: each geo's Trends score in the
// snapshot's latest complete week. Every geo's score is indexed to its own
// 5-year peak (= 100), so it says how close the term is to its local high,
// not how much demand the geo has: scores are not comparable across geos and
// no bid modifier is derived from them. A null score (too little search
// volume for Trends to report) shows as "insufficient data" and stays out of
// the chart.

const CHART_BARS = 15;
const SCORE_NOTE = 'Index vs. this region’s own 5-year peak — not comparable across regions';

type ScoredGeoRow = SemGeoRow & { score: number };

interface GeoPanelProps {
  market: SemMarket;
  refreshDate: string;
  geo: string; // country code in global mode; unused for us (always national)
  term: string;
  source?: string;
}

export default function GeoPanel({ market, refreshDate, geo, term, source = '' }: GeoPanelProps) {
  const [rows, setRows] = useState<SemGeoRow[]>([]);
  const [week, setWeek] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!term || !refreshDate || (market === 'global' && (!geo || geo.length !== 2))) return;
    let cancelled = false;
    setLoading(true);
    setError('');
    fetchSemGeo(market, refreshDate, geo, term, source)
      .then(d => {
        if (cancelled) return;
        setRows(d.rows);
        setWeek(d.week);
      })
      .catch(e => {
        if (!cancelled) setError(e.response?.data || e.message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [market, refreshDate, geo, term, source]);

  // Geos Trends reported a score for; rows arrive sorted by score with the
  // nulls last, so this keeps the strongest geos first.
  const scored = useMemo(() => rows.filter((r): r is ScoredGeoRow => r.score !== null), [rows]);
  const risingRank = useMemo(() => rows.find(r => r.rising_rank > 0)?.rising_rank ?? 0, [rows]);

  const geoUnit = market === 'us' ? 'DMA' : 'Region';
  const geoNoun = market === 'us' ? 'DMA' : 'region';
  const showWeek = !loading && !error && week !== '';

  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <h3 className="text-sm font-semibold text-white mb-1 flex items-center gap-2">
        <MapPin size={14} className="text-emerald-400" /> Geo Interest
        {risingRank > 0 && (
          <span className="text-[10px] font-mono font-normal px-1.5 py-0.5 rounded border border-amber-500/30 bg-amber-500/5 text-amber-400">
            {market === 'us' ? 'National' : 'Country'} rising rank #{risingRank}
          </span>
        )}
      </h3>
      <p className="text-xs text-zinc-500 mb-4">
        Search interest in <span className="text-zinc-300">“{term}”</span> by {geoNoun}
        {showWeek && (
          <>
            {' '}in the week of <span className="font-mono text-zinc-400">{week}</span> (latest complete
            week in this snapshot; {scored.length} of {rows.length} {geoNoun}s have data)
          </>
        )}.
        Each score is indexed to that {geoNoun}’s own 5-year peak (= 100) — not comparable
        across {geoNoun}s, and not a bid recommendation.
      </p>

      {error && <ErrorBanner message={error} />}
      {loading && <div className="h-64 rounded-xl animate-pulse" style={{ background: '#0c0c0f' }} />}

      {!loading && !error && (rows.length > 0 ? (
        <>
          {scored.length > 0 && (
            <div className="h-56 mb-4">
              <ReactECharts option={geoBarOption(scored.slice(0, CHART_BARS))} style={{ height: '100%' }} notMerge />
            </div>
          )}
          <div className="overflow-y-auto max-h-64">
            <table className="w-full text-xs">
              <thead className="sticky top-0" style={{ background: '#111114' }}>
                <tr className="text-left text-[10px] font-mono uppercase text-zinc-600 border-b border-zinc-800/60">
                  <th className="py-2 pr-4">{geoUnit}</th>
                  <th className="py-2 pr-4 text-right">
                    <span title={SCORE_NOTE} className="cursor-help underline decoration-dotted underline-offset-2">
                      Score
                    </span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map(r => (
                  <tr key={r.geo} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                    <td className="py-1.5 pr-4 text-zinc-300">{r.geo}</td>
                    <td className="py-1.5 pr-4 text-right font-mono text-zinc-400">
                      {r.score !== null ? r.score : (
                        <span className="font-sans italic text-zinc-600"
                          title="Too little search volume this week for Google Trends to report a score">
                          insufficient data
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      ) : (
        <EmptyState text={`No ${geoUnit.toLowerCase()}-level data for “${term}” in this snapshot`} />
      ))}
    </div>
  );
}

function geoBarOption(rows: ScoredGeoRow[]) {
  // Reverse so the highest score renders at the top of the horizontal bars.
  const ordered = [...rows].reverse();
  return {
    backgroundColor: 'transparent',
    tooltip: {
      ...CHART_TOOLTIP,
      // Geo names come from the dataset: escape them, the tooltip is HTML.
      formatter: (p: { name: string; value: number }) =>
        `<div style="font-weight:600;color:#f4f4f5;font-size:12px">${format.encodeHTML(p.name)}</div>
        <div style="color:#a1a1aa">Score: ${p.value} / 100</div>
        <div style="color:#71717a;font-size:11px">${SCORE_NOTE}</div>`,
    },
    grid: { left: 8, right: 24, top: 8, bottom: 24, containLabel: true },
    xAxis: { type: 'value', max: 100, axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE },
    yAxis: {
      type: 'category',
      data: ordered.map(r => r.geo),
      axisLabel: { ...AXIS_LABEL, width: 140, overflow: 'truncate' },
    },
    series: [{
      type: 'bar',
      data: ordered.map(r => r.score),
      itemStyle: { color: 'rgba(52,211,153,0.7)', borderRadius: [0, 3, 3, 0] },
      barMaxWidth: 12,
    }],
  };
}

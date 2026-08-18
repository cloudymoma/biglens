import { useState, useEffect, useMemo } from 'react';
import ReactECharts from 'echarts-for-react';
import { MapPin } from 'lucide-react';
import type { SemMarket, SemGeoRow } from '../../types';
import { fetchSemGeo } from '../../api';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import { CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE } from './shared';

// W2 — Geo Demand & Bid Modifier table for the selected term. The suggested
// modifier maps relative demand onto Google Ads' actual location-adjustment
// bounds: clamp((geo_score / avg_score − 1) × 100, −90, +900).

const MODIFIER_MIN = -90;
const MODIFIER_MAX = 900;
const CHART_BARS = 15;

function suggestedModifier(score: number, avg: number): number {
  if (avg <= 0) return 0;
  const raw = Math.round((score / avg - 1) * 100);
  return Math.min(MODIFIER_MAX, Math.max(MODIFIER_MIN, raw));
}

interface GeoPanelProps {
  market: SemMarket;
  refreshDate: string;
  geo: string; // country code in global mode; unused for us (always national)
  term: string;
}

export default function GeoPanel({ market, refreshDate, geo, term }: GeoPanelProps) {
  const [rows, setRows] = useState<SemGeoRow[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!term || !refreshDate || (market === 'global' && !geo)) return;
    setLoading(true);
    setError('');
    fetchSemGeo(market, refreshDate, geo, term)
      .then(d => setRows(d.rows))
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [market, refreshDate, geo, term]);

  // Average across geos with any signal; geos scoring above it get a positive
  // suggested modifier.
  const avg = useMemo(() => {
    const scored = rows.filter(r => r.score > 0);
    return scored.length > 0 ? scored.reduce((s, r) => s + r.score, 0) / scored.length : 0;
  }, [rows]);

  const geoUnit = market === 'us' ? 'DMA' : 'Region';

  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <h3 className="text-sm font-semibold text-white mb-1 flex items-center gap-2">
        <MapPin size={14} className="text-emerald-400" /> Geo Demand & Bid Modifiers
      </h3>
      <p className="text-xs text-zinc-500 mb-4">
        Where <span className="text-zinc-300">“{term}”</span> demand concentrates.
        Suggested modifiers are relative demand, not conversion data — bounds match
        Google Ads location adjustments (−90%…+900%).
      </p>

      {error && <ErrorBanner message={error} />}
      {loading && <div className="h-64 rounded-xl animate-pulse" style={{ background: '#0c0c0f' }} />}

      {!loading && !error && (rows.length > 0 ? (
        <>
          <div className="h-56 mb-4">
            <ReactECharts option={geoBarOption(rows.slice(0, CHART_BARS), avg)} style={{ height: '100%' }} notMerge />
          </div>
          <div className="overflow-y-auto max-h-64">
            <table className="w-full text-xs">
              <thead className="sticky top-0" style={{ background: '#111114' }}>
                <tr className="text-left text-[10px] font-mono uppercase text-zinc-600 border-b border-zinc-800/60">
                  <th className="py-2 pr-4">{geoUnit}</th>
                  <th className="py-2 pr-4 text-right">Score</th>
                  <th className="py-2 pr-4 text-right">Suggested Modifier</th>
                  <th className="py-2 pr-4 text-right">Rising Rank</th>
                </tr>
              </thead>
              <tbody>
                {rows.map(r => {
                  const mod = suggestedModifier(r.score, avg);
                  return (
                    <tr key={r.geo} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                      <td className="py-1.5 pr-4 text-zinc-300">{r.geo}</td>
                      <td className="py-1.5 pr-4 text-right font-mono text-zinc-400">{r.score}</td>
                      <td className={`py-1.5 pr-4 text-right font-mono ${
                        mod > 0 ? 'text-emerald-400' : mod < 0 ? 'text-rose-400' : 'text-zinc-500'
                      }`}>
                        {mod > 0 ? `+${mod}%` : `${mod}%`}
                      </td>
                      <td className="py-1.5 pr-4 text-right font-mono text-zinc-500">
                        {r.rising_rank > 0 ? `#${r.rising_rank}` : '—'}
                      </td>
                    </tr>
                  );
                })}
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

function geoBarOption(rows: SemGeoRow[], avg: number) {
  // Reverse so the strongest geo renders at the top of the horizontal bars.
  const ordered = [...rows].reverse();
  return {
    backgroundColor: 'transparent',
    tooltip: {
      ...CHART_TOOLTIP,
      formatter: (p: { name: string; value: number }) => {
        const mod = suggestedModifier(p.value, avg);
        return `<div style="font-weight:600;color:#f4f4f5;font-size:12px">${p.name}</div>
          <div style="color:#a1a1aa">Score: ${p.value}</div>
          <div style="color:${mod >= 0 ? '#34d399' : '#fb7185'}">Suggested modifier: ${mod > 0 ? '+' : ''}${mod}%</div>`;
      },
    },
    grid: { left: 8, right: 24, top: 8, bottom: 24, containLabel: true },
    xAxis: { type: 'value', axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE },
    yAxis: {
      type: 'category',
      data: ordered.map(r => r.geo),
      axisLabel: { ...AXIS_LABEL, width: 140, overflow: 'truncate' },
    },
    series: [{
      type: 'bar',
      data: ordered.map(r => ({
        value: r.score,
        itemStyle: {
          color: suggestedModifier(r.score, avg) >= 0 ? 'rgba(52,211,153,0.7)' : 'rgba(251,113,133,0.6)',
          borderRadius: [0, 3, 3, 0],
        },
      })),
      barMaxWidth: 12,
    }],
  };
}

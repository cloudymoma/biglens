import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { LineChart, Newspaper, X } from 'lucide-react';
import type { SemMarket, SemHistoryPoint, GdeltGkgData } from '../../types';
import { fetchSemTerm, fetchGdeltGkg } from '../../api';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import { CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE } from './shared';

// W6 — Term drill-down: full weekly history from the pinned snapshot (top-25
// and rising tables unioned, so brand-new arbitrage terms show whatever
// history exists). Top grid: seasonality curve with a same-week-last-year
// marker for pacing; bottom grid: week-over-week Δscore for the last 8 weeks.

const WOW_WEEKS = 8;
const YEAR_DAYS_MS = 364 * 86_400_000; // 52 whole weeks, keeps the weekday aligned

interface TermPanelProps {
  market: SemMarket;
  refreshDate: string;
  geo: string;
  term: string;
  onClose: () => void;
}

export default function TermPanel({ market, refreshDate, geo, term, onClose }: TermPanelProps) {
  const [history, setHistory] = useState<SemHistoryPoint[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!term || !refreshDate || (market === 'global' && !geo)) return;
    setLoading(true);
    setError('');
    fetchSemTerm(market, refreshDate, geo, term)
      .then(d => setHistory(d.history))
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [market, refreshDate, geo, term]);

  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <div className="flex items-start justify-between gap-3 mb-1">
        <h3 className="text-sm font-semibold text-white flex items-center gap-2">
          <LineChart size={14} className="text-cyan-400" /> Term Drill-Down:
          <span className="text-cyan-400">“{term}”</span>
        </h3>
        <button
          onClick={onClose}
          className="text-zinc-600 hover:text-zinc-300 transition-colors cursor-pointer"
          title="Close drill-down"
        >
          <X size={14} />
        </button>
      </div>
      <p className="text-xs text-zinc-500 mb-4">
        Weekly search score from this snapshot's history — seasonality for budget pacing
        (◆ marks the same week last year), plus week-over-week momentum below.
      </p>

      {error && <ErrorBanner message={error} />}
      {loading && <div className="h-72 rounded-xl animate-pulse" style={{ background: '#0c0c0f' }} />}

      {!loading && !error && (history.length > 1 ? (
        <div className="h-[340px]">
          <ReactECharts option={termOption(history)} style={{ height: '100%' }} notMerge />
        </div>
      ) : (
        <EmptyState text="No weekly history in this snapshot yet — the term is brand new (often the strongest arbitrage signal)" />
      ))}

      <NewsCycleContext />
    </div>
  );
}

// "Why is this trending?" — on-demand only (GKG partitions are GB-scale;
// the query is the one the GDELT dashboard already runs). GKG can't be
// filtered to a term or market affordably, so this is global news-cycle
// context to eyeball against the term, labeled as such.
function NewsCycleContext() {
  const [gkg, setGkg] = useState<GdeltGkgData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [requested, setRequested] = useState(false);

  function load() {
    setRequested(true);
    setLoading(true);
    setError('');
    const end = new Date().toISOString().slice(0, 10);
    const start = new Date(Date.now() - 2 * 86_400_000).toISOString().slice(0, 10);
    fetchGdeltGkg(start, end)
      .then(setGkg)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }

  return (
    <div className="mt-4 border-t border-zinc-800/40 pt-4">
      {!requested ? (
        <button
          onClick={load}
          className="flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-lg border border-violet-500/30 text-violet-400 hover:bg-violet-500/10 transition-colors cursor-pointer"
        >
          <Newspaper size={12} /> Why is this trending? (news-cycle context)
        </button>
      ) : (
        <>
          <p className="text-[10px] font-mono uppercase text-zinc-600 mb-2">
            Global news cycle — GDELT GKG themes &amp; people, last 3 days (not term-filtered)
          </p>
          {error && <ErrorBanner message={error} />}
          {loading && <div className="h-16 rounded-xl animate-pulse" style={{ background: '#0c0c0f' }} />}
          {gkg && (
            <div className="space-y-2">
              <div className="flex flex-wrap gap-1.5">
                {gkg.themes.slice(0, 14).map(t => (
                  <span key={t.name} title={`${t.name} · ${t.article_count.toLocaleString()} articles`}
                    className="text-[10px] px-2 py-0.5 rounded border border-zinc-700/50 text-zinc-400">
                    {prettyTheme(t.name)}
                  </span>
                ))}
              </div>
              <div className="flex flex-wrap gap-1.5">
                {gkg.persons.slice(0, 8).map(p => (
                  <span key={p.name} title={`${p.article_count.toLocaleString()} articles`}
                    className="text-[10px] px-2 py-0.5 rounded border border-violet-500/20 text-violet-300">
                    {p.name}
                  </span>
                ))}
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// Same display cleanup as the GDELT dashboard: TAX_FNCACT_PRESIDENT /
// WB_2670_JOBS read badly raw; keep the raw code in the tooltip.
function prettyTheme(name: string) {
  return name.replace(/^WB_\d+_/, '').replace(/_/g, ' ');
}

function termOption(history: SemHistoryPoint[]) {
  const weeks = history.map(p => p.week);
  const scores = history.map(p => p.score);

  // Same-week-last-year marker: closest historical point to (last week − 52 weeks).
  const lastMs = Date.parse(weeks[weeks.length - 1]);
  const targetMs = lastMs - YEAR_DAYS_MS;
  let markIdx = -1;
  let best = Infinity;
  history.forEach((p, i) => {
    const d = Math.abs(Date.parse(p.week) - targetMs);
    if (d < best) { best = d; markIdx = i; }
  });
  const hasYearMark = best <= 14 * 86_400_000; // only mark if within two weeks of target

  const wow = history.slice(-WOW_WEEKS - 1).map((p, i, arr) =>
    i === 0 ? null : { week: p.week, delta: p.score - arr[i - 1].score },
  ).filter((d): d is { week: string; delta: number } => d !== null);

  return {
    backgroundColor: 'transparent',
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    axisPointer: { link: [{ xAxisIndex: 'all' }] },
    grid: [
      { left: 40, right: 24, top: 16, height: '52%' },
      { left: 40, right: 24, bottom: 24, height: '20%' },
    ],
    xAxis: [
      { type: 'category', gridIndex: 0, data: weeks, axisLabel: { ...AXIS_LABEL, show: false }, axisTick: { show: false } },
      { type: 'category', gridIndex: 1, data: wow.map(d => d.week), axisLabel: AXIS_LABEL },
    ],
    yAxis: [
      { type: 'value', gridIndex: 0, max: 100, axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE, name: 'Score', nameTextStyle: { color: '#52525b', fontSize: 10 } },
      { type: 'value', gridIndex: 1, axisLabel: AXIS_LABEL, splitLine: { show: false }, name: 'WoW Δ', nameTextStyle: { color: '#52525b', fontSize: 10 } },
    ],
    series: [
      {
        name: 'Weekly score',
        type: 'line',
        xAxisIndex: 0,
        yAxisIndex: 0,
        data: scores,
        showSymbol: false,
        lineStyle: { color: '#22d3ee', width: 1.5 },
        areaStyle: { color: 'rgba(34,211,238,0.08)' },
        markPoint: hasYearMark ? {
          symbol: 'diamond',
          symbolSize: 10,
          itemStyle: { color: '#a78bfa' },
          label: { show: false },
          data: [{
            name: 'Same week last year',
            coord: [markIdx, scores[markIdx]],
          }],
        } : undefined,
      },
      {
        name: 'WoW Δscore',
        type: 'bar',
        xAxisIndex: 1,
        yAxisIndex: 1,
        data: wow.map(d => ({
          value: d.delta,
          itemStyle: { color: d.delta >= 0 ? 'rgba(52,211,153,0.7)' : 'rgba(251,113,133,0.6)', borderRadius: 2 },
        })),
        barMaxWidth: 16,
      },
    ],
  };
}

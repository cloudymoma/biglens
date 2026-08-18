import ReactECharts from 'echarts-for-react';
import { ShieldAlert } from 'lucide-react';
import type { SemSafetyRow } from '../../types';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import { CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE } from './shared';

// W3 — Brand Safety & Tone Radar: 14 days of GDELT news tone + conflict
// share for the market. GDELT tone is country-grained (actor codes), so this
// is always a market-level signal — US-national in DMA mode, per selected
// country in global mode. Data is fetched by SemDashboard so the same status
// drives the W1/W4 overlay.

export type SafetyLevel = 'green' | 'amber' | 'red';

export interface SafetyStatus {
  level: SafetyLevel;
  tone: number; // event-weighted 3-day average
  conflictShare: number;
}

// Thresholds from the design (unified-view semantics): red when the 3-day
// tone < −2 or conflict share > 30%, amber when tone < −1.
export function computeSafetyStatus(rows: SemSafetyRow[]): SafetyStatus | null {
  const last = rows.slice(-3);
  const n = last.reduce((s, r) => s + r.event_count, 0);
  if (n === 0) return null;
  const tone = last.reduce((s, r) => s + r.avg_tone * r.event_count, 0) / n;
  const conflictShare = last.reduce((s, r) => s + r.conflict_share * r.event_count, 0) / n;
  const level: SafetyLevel = tone < -2 || conflictShare > 0.3 ? 'red' : tone < -1 ? 'amber' : 'green';
  return { level, tone, conflictShare };
}

const BANNER_STYLES: Record<SafetyLevel, string> = {
  green: 'border-emerald-500/30 bg-emerald-500/5 text-emerald-300',
  amber: 'border-amber-500/30 bg-amber-500/5 text-amber-300',
  red: 'border-rose-500/30 bg-rose-500/5 text-rose-300',
};

function bannerText(status: SafetyStatus): string {
  const tone = status.tone.toFixed(1);
  const conflict = `${Math.round(status.conflictShare * 100)}%`;
  switch (status.level) {
    case 'green':
      return `🟢 Calm news cycle (3-day tone ${tone}, conflict share ${conflict}) — no market-level brand-safety objection to trend bidding.`;
    case 'amber':
      return `🟡 Mildly negative news tone (${tone}) — review broad match before bidding on news-adjacent terms.`;
    case 'red':
      return `🔴 Negative news cycle (tone ${tone}, conflict share ${conflict}) — review broad match, consider pausing brand-adjacent trend bids, add negatives in the table.`;
  }
}

interface SafetyPanelProps {
  rows: SemSafetyRow[];
  status: SafetyStatus | null;
  loading: boolean;
  error: string;
  marketLabel: string;
}

export default function SafetyPanel({ rows, status, loading, error, marketLabel }: SafetyPanelProps) {
  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <h3 className="text-sm font-semibold text-white mb-1 flex items-center gap-2">
        <ShieldAlert size={14} className="text-amber-400" /> Brand Safety & Tone Radar
      </h3>
      <p className="text-xs text-zinc-500 mb-4">
        14 days of GDELT news tone and conflict-event share for {marketLabel}.
        Country-grained news signal — market context, not per-keyword sentiment.
      </p>

      {error && <ErrorBanner message={error} />}
      {loading && <div className="h-64 rounded-xl animate-pulse" style={{ background: '#0c0c0f' }} />}

      {!loading && !error && (rows.length > 0 && status ? (
        <>
          <div className={`rounded-xl border px-4 py-3 text-xs leading-relaxed mb-4 ${BANNER_STYLES[status.level]}`}>
            {bannerText(status)}
          </div>
          <div className="h-56">
            <ReactECharts option={safetyOption(rows)} style={{ height: '100%' }} notMerge />
          </div>
        </>
      ) : (
        <EmptyState text={`No GDELT news events for ${marketLabel} in the last 14 days`} />
      ))}
    </div>
  );
}

function safetyOption(rows: SemSafetyRow[]) {
  return {
    backgroundColor: 'transparent',
    tooltip: { ...CHART_TOOLTIP, trigger: 'axis' },
    legend: {
      textStyle: { color: '#71717a', fontSize: 10 },
      top: 0,
      itemWidth: 12,
      itemHeight: 8,
    },
    grid: { left: 40, right: 44, top: 28, bottom: 24 },
    xAxis: {
      type: 'category',
      data: rows.map(r => r.ingest_date.slice(5)),
      axisLabel: AXIS_LABEL,
    },
    yAxis: [
      {
        type: 'value',
        name: 'Tone',
        nameTextStyle: { color: '#52525b', fontSize: 10 },
        axisLabel: AXIS_LABEL,
        splitLine: SPLIT_LINE,
      },
      {
        type: 'value',
        name: 'Conflict %',
        nameTextStyle: { color: '#52525b', fontSize: 10 },
        axisLabel: { ...AXIS_LABEL, formatter: (v: number) => `${v}%` },
        splitLine: { show: false },
      },
    ],
    series: [
      {
        name: 'Avg tone',
        type: 'line',
        data: rows.map(r => r.avg_tone),
        showSymbol: false,
        lineStyle: { color: '#fbbf24', width: 1.5 },
        markLine: {
          silent: true,
          symbol: 'none',
          lineStyle: { color: '#f43f5e', type: 'dashed', width: 1 },
          label: { color: '#9f1239', fontSize: 9, formatter: 'red threshold' },
          data: [{ yAxis: -2 }],
        },
      },
      {
        name: 'Conflict share',
        type: 'bar',
        yAxisIndex: 1,
        data: rows.map(r => Math.round(r.conflict_share * 1000) / 10),
        itemStyle: { color: 'rgba(244,63,94,0.35)', borderRadius: [2, 2, 0, 0] },
        barMaxWidth: 10,
      },
    ],
  };
}

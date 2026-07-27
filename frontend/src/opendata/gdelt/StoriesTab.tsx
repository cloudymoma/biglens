import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import type { GdeltStoriesData, GdeltStoryRow } from '../../types';
import { fetchGdeltStories } from '../../api';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import {
  MAX_STORIES_DAYS, CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE,
  toneColor, spanOf, Section, SourceLink, LoadingPulse, RangeTooWide,
} from './shared';
import { cameoFullLabel } from './cameo';

// One-line story description assembled from the event fields.
function storyTitle(s: GdeltStoryRow): string {
  const actors = [s.actor1, s.actor2].filter(Boolean).join(' → ');
  const label = cameoFullLabel(s.event_code);
  return actors ? `${actors}: ${label}` : label;
}

// Mentions per hour over the story's observed lifetime; short-lived spikes
// use a 1-hour floor so a burst caught in one 15-min window doesn't explode.
function mentionsPerHour(s: GdeltStoryRow): number {
  return s.mentions / Math.max(s.span_minutes / 60, 1);
}

export default function StoriesTab({ startDate, endDate }: { startDate: string; endDate: string }) {
  const [data, setData] = useState<GdeltStoriesData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const tooWide = spanOf(startDate, endDate) > MAX_STORIES_DAYS;

  useEffect(() => {
    if (tooWide) return;
    setLoading(true);
    setError('');
    fetchGdeltStories(startDate, endDate)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [startDate, endDate, tooWide]);

  if (tooWide) return <RangeTooWide maxDays={MAX_STORIES_DAYS} what="Story velocity (mentions stream)" />;
  if (error) return <ErrorBanner message={error} />;
  if (loading || !data) return <LoadingPulse />;
  if (data.stories.length === 0) return <EmptyState text="No stories in this range" />;

  return (
    <div className="space-y-6">
      <Section
        title="Widest-Spreading Stories"
        note="Top 10 by distinct outlets — spread across independent sources, not raw mention volume"
      >
        <div className="h-[380px]">
          <ReactECharts option={spreadOption(data.stories.slice(0, 10))} style={{ height: '100%' }} notMerge />
        </div>
      </Section>

      <Section
        title="Story Board"
        note={`Top ${data.stories.length} events by outlet spread · mentions with GDELT confidence ≥ 40`}
      >
        <div className="max-h-[520px] overflow-y-auto">
          <table className="w-full text-xs">
            <thead className="sticky top-0" style={{ background: '#111114' }}>
              <tr className="text-left text-[10px] font-mono text-zinc-600 uppercase">
                <th className="py-2 pr-3 font-medium">Story</th>
                <th className="py-2 pr-3 font-medium">Location</th>
                <th className="py-2 pr-3 font-medium text-right">Outlets</th>
                <th className="py-2 pr-3 font-medium text-right">Mentions</th>
                <th className="py-2 pr-3 font-medium text-right">Mentions/h</th>
                <th className="py-2 pr-3 font-medium text-right">Conf.</th>
                <th className="py-2 pr-3 font-medium text-right">Tone</th>
                <th className="py-2 pr-3 font-medium">First seen</th>
                <th className="py-2 font-medium">Link</th>
              </tr>
            </thead>
            <tbody>
              {data.stories.map((s, i) => (
                <tr key={`${s.source_url}-${i}`} className="border-t border-zinc-800/40 text-zinc-400">
                  <td className="py-2 pr-3 max-w-[260px] truncate text-zinc-300" title={storyTitle(s)}>
                    {storyTitle(s)}
                  </td>
                  <td className="py-2 pr-3 max-w-[160px] truncate" title={s.location}>{s.location || '—'}</td>
                  <td className="py-2 pr-3 text-right font-mono text-white">{s.outlets.toLocaleString()}</td>
                  <td className="py-2 pr-3 text-right font-mono">{s.mentions.toLocaleString()}</td>
                  <td className="py-2 pr-3 text-right font-mono">{mentionsPerHour(s).toFixed(1)}</td>
                  <td className="py-2 pr-3 text-right font-mono">{s.avg_confidence.toFixed(0)}</td>
                  <td className="py-2 pr-3 text-right font-mono" style={{ color: toneColor(s.avg_tone) }}>
                    {s.avg_tone.toFixed(1)}
                  </td>
                  <td className="py-2 pr-3 font-mono text-[11px] whitespace-nowrap">{s.first_seen || '—'}</td>
                  <td className="py-2 max-w-[160px]"><SourceLink raw={s.source_url} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Section>

      <p className="text-[11px] text-zinc-600">
        Built from the GDELT mentions stream: every re-mention of an event across ~10k monitored
        outlets. Ranking by distinct outlets favors stories independently picked up worldwide over
        stories one syndicate repeats. Mentions/h averages over the story's observed lifetime
        within the window, so long-running stories read slower than fresh bursts.
      </p>
    </div>
  );
}

interface SpreadBarParam { data: { value: number; mentions: number; tone: number }; name: string }

function spreadOption(stories: GdeltStoryRow[]) {
  const sorted = [...stories].sort((a, b) => a.outlets - b.outlets);
  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      ...CHART_TOOLTIP,
      formatter: (params: SpreadBarParam[]) => {
        const p = params[0];
        return `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${p.name}</div>
                <div style="color:#f4f4f5;font-size:13px;font-weight:600">${p.data.value.toLocaleString()} outlets</div>
                <div style="color:#a1a1aa;font-size:11px">${p.data.mentions.toLocaleString()} mentions · tone ${p.data.tone.toFixed(1)}</div>`;
      },
    },
    grid: { left: 8, right: 56, bottom: 8, top: 8, containLabel: true },
    xAxis: {
      type: 'value',
      name: 'outlets',
      nameTextStyle: AXIS_LABEL,
      axisLabel: AXIS_LABEL,
      splitLine: SPLIT_LINE,
    },
    yAxis: {
      type: 'category',
      data: sorted.map(storyTitle),
      axisLabel: { ...AXIS_LABEL, fontSize: 10, width: 230, overflow: 'truncate' },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [{
      type: 'bar',
      data: sorted.map(s => ({
        value: s.outlets,
        mentions: s.mentions,
        tone: s.avg_tone,
        itemStyle: { color: toneColor(s.avg_tone), opacity: 0.85, borderRadius: [0, 4, 4, 0] },
      })),
      barMaxWidth: 16,
      label: {
        show: true, position: 'right', color: '#a1a1aa', fontSize: 9,
        fontFamily: 'JetBrains Mono, monospace',
        formatter: (p: { value: number }) => Number(p.value).toLocaleString(),
      },
    }],
  };
}

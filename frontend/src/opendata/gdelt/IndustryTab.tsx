import { useState, useEffect, useMemo } from 'react';
import ReactECharts from 'echarts-for-react';
import type {
  GdeltIndustryData, GdeltIndustryDaily, GdeltIndustryKey, GdeltNamedCount,
} from '../../types';
import { fetchGdeltIndustry } from '../../api';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import {
  MAX_GKG_DAYS, CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE,
  spanOf, toneColor, Section, SourceLink, LoadingPulse, RangeTooWide,
} from './shared';

const INDUSTRIES: { key: GdeltIndustryKey; label: string }[] = [
  { key: 'finance', label: 'Finance' },
  { key: 'retail', label: 'Retail & Consumer' },
  { key: 'biomedical', label: 'Bio-Medical' },
  { key: 'education', label: 'Education' },
];

function ToneChip({ tone }: { tone: number }) {
  return (
    <span className="font-mono text-[11px]" style={{ color: toneColor(tone) }}>
      {tone > 0 ? '+' : ''}{tone.toFixed(1)}
    </span>
  );
}

export default function IndustryTab({ startDate, endDate }: { startDate: string; endDate: string }) {
  const [industry, setIndustry] = useState<GdeltIndustryKey>('finance');
  const [data, setData] = useState<GdeltIndustryData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const tooWide = spanOf(startDate, endDate) > MAX_GKG_DAYS;

  useEffect(() => {
    if (tooWide) return;
    setLoading(true);
    setError('');
    fetchGdeltIndustry(startDate, endDate, industry)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [startDate, endDate, industry, tooWide]);

  // KPIs derived from the daily series: total articles, article-weighted
  // mean tone, and the most negative day in the window.
  const kpis = useMemo(() => {
    const daily = data?.daily ?? [];
    let articles = 0;
    let toneSum = 0;
    let worst: GdeltIndustryDaily | null = null;
    for (const d of daily) {
      articles += d.article_count;
      toneSum += d.avg_tone * d.article_count;
      if (!worst || d.avg_tone < worst.avg_tone) worst = d;
    }
    return { articles, avgTone: articles > 0 ? toneSum / articles : 0, worst };
  }, [data]);

  const label = INDUSTRIES.find(i => i.key === industry)?.label ?? industry;

  if (tooWide) return <RangeTooWide maxDays={MAX_GKG_DAYS} what="Industry pulse analysis (GKG)" />;
  if (error) return <ErrorBanner message={error} />;
  if (loading || !data) return <LoadingPulse />;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-4">
        <div>
          <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">Industry</label>
          <select
            value={industry}
            onChange={e => setIndustry(e.target.value as GdeltIndustryKey)}
            className="text-xs text-zinc-300 rounded-lg px-3 py-2 outline-none cursor-pointer border border-zinc-800/50"
            style={{ background: '#09090b' }}
          >
            {INDUSTRIES.map(i => (
              <option key={i.key} value={i.key}>{i.label}</option>
            ))}
          </select>
        </div>
        <p className="text-[10px] text-zinc-600 self-end pb-2 max-w-[420px] leading-relaxed">
          A vertical is a curated slice of GDELT GKG themes — coverage varies by
          industry (Retail & Consumer runs thin).
        </p>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div className="rounded-2xl border border-zinc-800/50 p-4" style={{ background: '#111114' }}>
          <p className="text-[10px] font-mono uppercase text-cyan-400">Articles</p>
          <p className="text-lg font-semibold text-white font-mono">{kpis.articles.toLocaleString()}</p>
          <p className="text-[10px] text-zinc-600">{label} coverage in range</p>
        </div>
        <div className="rounded-2xl border border-zinc-800/50 p-4" style={{ background: '#111114' }}>
          <p className="text-[10px] font-mono uppercase" style={{ color: toneColor(kpis.avgTone) }}>Average Tone</p>
          <p className="text-lg font-semibold text-white font-mono">{kpis.avgTone.toFixed(2)}</p>
          <p className="text-[10px] text-zinc-600">article-weighted, −10 grim to +10 upbeat</p>
        </div>
        <div className="rounded-2xl border border-zinc-800/50 p-4" style={{ background: '#111114' }}>
          <p className="text-[10px] font-mono uppercase text-rose-400">Most Negative Day</p>
          <p className="text-lg font-semibold text-white font-mono">
            {kpis.worst ? `${kpis.worst.ingest_date.slice(5)} (${kpis.worst.avg_tone.toFixed(1)})` : '—'}
          </p>
          <p className="text-[10px] text-zinc-600">lowest daily average tone</p>
        </div>
      </div>

      <Section
        title={`${label} Daily Pulse`}
        note="Articles per day carrying the vertical's themes, with average tone on the right axis"
      >
        {data.daily.length > 0 ? (
          <div className="h-[300px]">
            <ReactECharts option={dailyOption(data.daily)} style={{ height: '100%' }} notMerge />
          </div>
        ) : (
          <EmptyState text="No articles for this vertical in the range — GDELT coverage may be thin" />
        )}
      </Section>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <Section title="Companies in the News" note="Articles naming each organization, with average article tone">
          {data.orgs.length > 0 ? (
            <div className="max-h-[440px] overflow-y-auto">
              <table className="w-full text-xs">
                <thead className="sticky top-0" style={{ background: '#111114' }}>
                  <tr className="text-left text-[10px] font-mono text-zinc-600 uppercase">
                    <th className="py-2 pr-3 font-medium">Organization</th>
                    <th className="py-2 pr-3 font-medium text-right">Articles</th>
                    <th className="py-2 font-medium text-right">Tone</th>
                  </tr>
                </thead>
                <tbody>
                  {data.orgs.map(o => (
                    <tr key={o.name} className="border-t border-zinc-800/40 text-zinc-400">
                      <td className="py-2 pr-3 max-w-[220px] truncate capitalize" title={o.name}>{o.name}</td>
                      <td className="py-2 pr-3 text-right font-mono text-white">{o.article_count.toLocaleString()}</td>
                      <td className="py-2 text-right"><ToneChip tone={o.avg_tone} /></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <EmptyState text="No organizations in this range" />
          )}
        </Section>

        <Section title="Sub-topics" note="Which of the vertical's themes drive the coverage">
          {data.subtopics.length > 0 ? (
            <div className="h-[440px]">
              <ReactECharts option={subtopicsOption(data.subtopics)} style={{ height: '100%' }} notMerge />
            </div>
          ) : (
            <EmptyState text="No sub-topics in this range" />
          )}
        </Section>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <Section title="Top Outlets" note="Most active sources covering the vertical, with their average tone">
          {data.outlets.length > 0 ? (
            <table className="w-full text-xs">
              <thead>
                <tr className="text-left text-[10px] font-mono text-zinc-600 uppercase">
                  <th className="py-2 pr-3 font-medium">Outlet</th>
                  <th className="py-2 pr-3 font-medium text-right">Articles</th>
                  <th className="py-2 font-medium text-right">Tone</th>
                </tr>
              </thead>
              <tbody>
                {data.outlets.map(s => (
                  <tr key={s.media_source} className="border-t border-zinc-800/40 text-zinc-400">
                    <td className="py-2 pr-3 max-w-[220px] truncate" title={s.media_source}>{s.media_source}</td>
                    <td className="py-2 pr-3 text-right font-mono text-white">{s.article_count.toLocaleString()}</td>
                    <td className="py-2 text-right"><ToneChip tone={s.avg_tone} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <EmptyState text="No outlets in this range" />
          )}
        </Section>

        <Section title="Most Negative Articles" note="The vertical's risk feed — lowest-tone articles in the window">
          {data.articles.length > 0 ? (
            <div className="max-h-[440px] overflow-y-auto">
              <table className="w-full text-xs">
                <thead className="sticky top-0" style={{ background: '#111114' }}>
                  <tr className="text-left text-[10px] font-mono text-zinc-600 uppercase">
                    <th className="py-2 pr-3 font-medium">Date</th>
                    <th className="py-2 pr-3 font-medium">Article</th>
                    <th className="py-2 font-medium text-right">Tone</th>
                  </tr>
                </thead>
                <tbody>
                  {data.articles.map((a, i) => (
                    <tr key={`${a.url}-${i}`} className="border-t border-zinc-800/40 text-zinc-400">
                      <td className="py-2 pr-3 font-mono whitespace-nowrap">{a.ingest_date.slice(5)}</td>
                      <td className="py-2 pr-3 max-w-[240px]"><SourceLink raw={a.url} /></td>
                      <td className="py-2 text-right"><ToneChip tone={a.tone} /></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <EmptyState text="No articles in this range" />
          )}
        </Section>
      </div>
    </div>
  );
}

function dailyOption(daily: GdeltIndustryDaily[]) {
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
      {
        type: 'value',
        name: 'articles',
        nameTextStyle: AXIS_LABEL,
        axisLabel: { ...AXIS_LABEL, formatter: (v: number) => (v >= 1000 ? `${v / 1000}k` : v) },
        splitLine: SPLIT_LINE,
      },
      {
        type: 'value',
        name: 'tone',
        nameTextStyle: AXIS_LABEL,
        axisLabel: AXIS_LABEL,
        splitLine: { show: false },
      },
    ],
    series: [
      {
        name: 'Articles',
        type: 'bar',
        data: daily.map(d => d.article_count),
        barMaxWidth: 22,
        itemStyle: { color: '#0e7490', opacity: 0.85 },
      },
      {
        name: 'Avg tone',
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        showSymbol: daily.length < 32,
        data: daily.map(d => d.avg_tone),
        lineStyle: { color: '#fbbf24', width: 2 },
        itemStyle: { color: '#fbbf24' },
      },
    ],
  };
}

function subtopicsOption(subtopics: GdeltNamedCount[]) {
  const sorted = [...subtopics].sort((a, b) => a.article_count - b.article_count);
  return {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
    grid: { left: 8, right: 56, bottom: 8, top: 8, containLabel: true },
    xAxis: {
      type: 'value',
      axisLabel: { ...AXIS_LABEL, formatter: (v: number) => (v >= 1000 ? `${v / 1000}k` : v) },
      splitLine: SPLIT_LINE,
    },
    yAxis: {
      type: 'category',
      data: sorted.map(s => s.name),
      axisLabel: { ...AXIS_LABEL, fontSize: 9 },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [{
      type: 'bar',
      data: sorted.map(s => s.article_count),
      barMaxWidth: 14,
      itemStyle: {
        color: {
          type: 'linear', x: 0, y: 0, x2: 1, y2: 0,
          colorStops: [
            { offset: 0, color: '#155e75' },
            { offset: 1, color: '#22d3ee' },
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

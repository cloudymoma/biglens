import { useState, useEffect, useMemo } from 'react';
import ReactECharts from 'echarts-for-react';
import type { GdeltImpactData, GdeltImpactDaily, GdeltImpactCountry } from '../../types';
import { fetchGdeltImpact } from '../../api';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import {
  MAX_GKG_DAYS, CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE,
  spanOf, Section, SourceLink, LoadingPulse, RangeTooWide,
} from './shared';

// Display order and colors for the core V2Counts impact types.
const IMPACT_TYPES: { type: string; label: string; color: string }[] = [
  { type: 'KILL', label: 'Killed', color: '#ef4444' },
  { type: 'WOUND', label: 'Wounded', color: '#f97316' },
  { type: 'ARREST', label: 'Arrested', color: '#eab308' },
  { type: 'KIDNAP', label: 'Kidnapped', color: '#a78bfa' },
  { type: 'DISPLACED', label: 'Displaced', color: '#38bdf8' },
  { type: 'SEIZE', label: 'Seized', color: '#a1a1aa' },
];

const impactMeta = (type: string) =>
  IMPACT_TYPES.find(t => t.type === type) ?? { type, label: type, color: '#71717a' };

export default function ImpactTab({ startDate, endDate }: { startDate: string; endDate: string }) {
  const [data, setData] = useState<GdeltImpactData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const tooWide = spanOf(startDate, endDate) > MAX_GKG_DAYS;

  useEffect(() => {
    if (tooWide) return;
    setLoading(true);
    setError('');
    fetchGdeltImpact(startDate, endDate)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [startDate, endDate, tooWide]);

  const totals = useMemo(() => {
    const sum: Record<string, number> = {};
    for (const d of data?.daily ?? []) sum[d.count_type] = (sum[d.count_type] || 0) + d.article_count;
    return sum;
  }, [data]);

  if (tooWide) return <RangeTooWide maxDays={MAX_GKG_DAYS} what="Human impact analysis (GKG)" />;
  if (error) return <ErrorBanner message={error} />;
  if (loading || !data) return <LoadingPulse />;

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 md:grid-cols-6 gap-3">
        {IMPACT_TYPES.map(t => (
          <div key={t.type} className="rounded-2xl border border-zinc-800/50 p-4" style={{ background: '#111114' }}>
            <p className="text-[10px] font-mono uppercase" style={{ color: t.color }}>{t.label}</p>
            <p className="text-lg font-semibold text-white font-mono">{(totals[t.type] || 0).toLocaleString()}</p>
            <p className="text-[10px] text-zinc-600">articles reporting</p>
          </div>
        ))}
      </div>

      <Section
        title="Daily Impact Reporting"
        note="Articles per day carrying a numeric figure of each type — coverage volume, not casualty totals"
      >
        {data.daily.length > 0 ? (
          <div className="h-[320px]">
            <ReactECharts option={dailyStackOption(data.daily)} style={{ height: '100%' }} notMerge />
          </div>
        ) : (
          <EmptyState text="No impact reports in this range" />
        )}
      </Section>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <Section title="Most Affected Countries" note="Articles reporting any impact figure, by country (FIPS code)">
          {data.countries.length > 0 ? (
            <div className="h-[440px]">
              <ReactECharts option={countriesOption(data.countries)} style={{ height: '100%' }} notMerge />
            </div>
          ) : (
            <EmptyState text="No countries in this range" />
          )}
        </Section>

        <Section title="Most Reported Incidents" note="One row per (type, figure, location) — coverage dedups the same incident across outlets">
          {data.incidents.length > 0 ? (
            <div className="max-h-[440px] overflow-y-auto">
              <table className="w-full text-xs">
                <thead className="sticky top-0" style={{ background: '#111114' }}>
                  <tr className="text-left text-[10px] font-mono text-zinc-600 uppercase">
                    <th className="py-2 pr-3 font-medium">Type</th>
                    <th className="py-2 pr-3 font-medium text-right">Figure</th>
                    <th className="py-2 pr-3 font-medium">Location</th>
                    <th className="py-2 pr-3 font-medium text-right">Articles</th>
                    <th className="py-2 font-medium">Sample</th>
                  </tr>
                </thead>
                <tbody>
                  {data.incidents.map((inc, i) => {
                    const meta = impactMeta(inc.count_type);
                    return (
                      <tr key={`${inc.count_type}-${inc.num}-${inc.location}-${i}`} className="border-t border-zinc-800/40 text-zinc-400">
                        <td className="py-2 pr-3">
                          <span className="font-mono text-[11px]" style={{ color: meta.color }}>{meta.label}</span>
                        </td>
                        <td className="py-2 pr-3 text-right font-mono text-white">{inc.num.toLocaleString()}</td>
                        <td className="py-2 pr-3 max-w-[180px] truncate" title={inc.location}>{inc.location}</td>
                        <td className="py-2 pr-3 text-right font-mono">{inc.article_count.toLocaleString()}</td>
                        <td className="py-2 max-w-[160px]"><SourceLink raw={inc.sample_url} /></td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          ) : (
            <EmptyState text="No incidents in this range" />
          )}
        </Section>
      </div>

      <p className="text-[11px] text-zinc-600">
        Figures are extracted by GDELT from news text (GKG V2Counts) and are media-reported,
        duplicated across outlets and unverified — early casualty reporting is routinely revised.
        Every metric here counts articles carrying a figure, never the figures summed; use it to
        read coverage trends and surface incidents, not as a casualty database.
      </p>
    </div>
  );
}

function dailyStackOption(daily: GdeltImpactDaily[]) {
  const dates = [...new Set(daily.map(d => d.ingest_date))].sort();
  const byType = new Map<string, Map<string, number>>();
  for (const d of daily) {
    if (!byType.has(d.count_type)) byType.set(d.count_type, new Map());
    byType.get(d.count_type)!.set(d.ingest_date, d.article_count);
  }
  const present = IMPACT_TYPES.filter(t => byType.has(t.type));
  return {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
    legend: { top: 0, textStyle: { color: '#a1a1aa', fontSize: 10 }, icon: 'circle', itemWidth: 8, itemHeight: 8 },
    grid: { left: 8, right: 8, bottom: 8, top: 30, containLabel: true },
    xAxis: {
      type: 'category',
      data: dates.map(d => d.slice(5)),
      axisLabel: { ...AXIS_LABEL, fontSize: 9 },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    yAxis: {
      type: 'value',
      name: 'articles',
      nameTextStyle: AXIS_LABEL,
      axisLabel: { ...AXIS_LABEL, formatter: (v: number) => (v >= 1000 ? `${v / 1000}k` : v) },
      splitLine: SPLIT_LINE,
    },
    series: present.map(t => ({
      name: t.label,
      type: 'bar',
      stack: 'impact',
      data: dates.map(d => byType.get(t.type)!.get(d) || 0),
      barMaxWidth: 22,
      itemStyle: { color: t.color, opacity: 0.85 },
    })),
  };
}

function countriesOption(countries: GdeltImpactCountry[]) {
  const sorted = [...countries].sort((a, b) => a.article_count - b.article_count);
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
      data: sorted.map(c => c.fips_country),
      axisLabel: { ...AXIS_LABEL, fontSize: 10 },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [{
      type: 'bar',
      data: sorted.map(c => c.article_count),
      barMaxWidth: 14,
      itemStyle: {
        color: {
          type: 'linear', x: 0, y: 0, x2: 1, y2: 0,
          colorStops: [
            { offset: 0, color: '#7f1d1d' },
            { offset: 1, color: '#ef4444' },
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

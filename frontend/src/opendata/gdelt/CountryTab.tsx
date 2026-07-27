import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import type { GdeltDyadsData, GdeltCountryData, GdeltCountryDaily, GdeltCountryEventType } from '../../types';
import { fetchGdeltDyads, fetchGdeltCountry } from '../../api';
import { EmptyState, ErrorBanner } from '../../dashboards/shared';
import {
  CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE,
  toneColor, goldsteinColor, Section, SourceLink, LoadingPulse,
} from './shared';
import { cameoFullLabel } from './cameo';

// Everything on this tab is keyed on CAMEO actor country codes (USA, RUS,
// CHN, ...): "events involving X", not "events located in X".
export default function CountryTab({ startDate, endDate }: { startDate: string; endDate: string }) {
  const [dyads, setDyads] = useState<GdeltDyadsData | null>(null);
  const [dyadsLoading, setDyadsLoading] = useState(true);
  const [dyadsError, setDyadsError] = useState('');

  const [country, setCountry] = useState('');
  const [detail, setDetail] = useState<GdeltCountryData | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState('');

  useEffect(() => {
    setDyadsLoading(true);
    setDyadsError('');
    fetchGdeltDyads(startDate, endDate)
      .then(data => {
        setDyads(data);
        // Default to the most active country the first time data arrives.
        setCountry(prev => prev || data.countries[0]?.country || '');
      })
      .catch(e => setDyadsError(e.response?.data || e.message))
      .finally(() => setDyadsLoading(false));
  }, [startDate, endDate]);

  useEffect(() => {
    if (!country) return;
    setDetailLoading(true);
    setDetailError('');
    fetchGdeltCountry(startDate, endDate, country)
      .then(setDetail)
      .catch(e => setDetailError(e.response?.data || e.message))
      .finally(() => setDetailLoading(false));
  }, [startDate, endDate, country]);

  if (dyadsError) return <ErrorBanner message={dyadsError} />;
  if (dyadsLoading || !dyads) return <LoadingPulse />;

  return (
    <div className="space-y-6">
      <Section
        title="Global Tension Board"
        note="Busiest country pairs (both directions merged) · Goldstein < 0 = destabilizing interactions · click a code to drill down"
      >
        {dyads.dyads.length > 0 ? (
          <div className="max-h-[360px] overflow-y-auto">
            <table className="w-full text-xs">
              <thead className="sticky top-0" style={{ background: '#111114' }}>
                <tr className="text-left text-[10px] font-mono text-zinc-600 uppercase">
                  <th className="py-2 pr-3 font-medium">Pair</th>
                  <th className="py-2 pr-3 font-medium text-right">Events</th>
                  <th className="py-2 pr-3 font-medium text-right">Goldstein</th>
                  <th className="py-2 font-medium text-right">Tone</th>
                </tr>
              </thead>
              <tbody>
                {dyads.dyads.map(d => (
                  <tr key={`${d.country_a}-${d.country_b}`} className="border-t border-zinc-800/40 text-zinc-400">
                    <td className="py-2 pr-3 font-mono">
                      <CodeButton code={d.country_a} active={country} onSelect={setCountry} />
                      <span className="text-zinc-600 mx-1.5">↔</span>
                      <CodeButton code={d.country_b} active={country} onSelect={setCountry} />
                    </td>
                    <td className="py-2 pr-3 text-right font-mono">{d.event_count.toLocaleString()}</td>
                    <td className="py-2 pr-3 text-right font-mono" style={{ color: goldsteinColor(d.avg_goldstein) }}>
                      {d.avg_goldstein.toFixed(2)}
                    </td>
                    <td className="py-2 text-right font-mono" style={{ color: toneColor(d.avg_tone) }}>
                      {d.avg_tone.toFixed(2)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState text="No country pairs in this range" />
        )}
      </Section>

      <div className="flex items-center gap-3">
        <label className="text-xs text-zinc-400">Drill down on</label>
        <select
          value={country}
          onChange={e => setCountry(e.target.value)}
          className="text-xs text-zinc-300 rounded-lg px-3 py-2 outline-none border border-zinc-800/50 focus:border-cyan-500/30"
          style={{ background: '#09090b' }}
        >
          {dyads.countries.map(c => (
            <option key={c.country} value={c.country}>
              {c.country} · {c.event_count.toLocaleString()} events
            </option>
          ))}
        </select>
        <span className="text-[10px] text-zinc-600">CAMEO actor code · events where either actor is this country</span>
      </div>

      {detailError && <ErrorBanner message={detailError} />}
      {detailLoading && <LoadingPulse />}
      {!detailLoading && !detailError && detail && (
        <>
          <Section title={`Daily Activity — ${detail.country}`} note="Events involving this country (bars) vs tone and Goldstein (lines)">
            {detail.daily.length > 0 ? (
              <div className="h-[280px]">
                <ReactECharts option={countryDailyOption(detail.daily)} style={{ height: '100%' }} notMerge />
              </div>
            ) : (
              <EmptyState text="No events in this range" />
            )}
          </Section>

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            <Section title="What Is Happening" note="Top event types, full CAMEO codes · red = destabilizing (Goldstein < 0)">
              {detail.event_types.length > 0 ? (
                <div className="h-[420px]">
                  <ReactECharts option={eventTypesOption(detail.event_types)} style={{ height: '100%' }} notMerge />
                </div>
              ) : (
                <EmptyState text="No event types" />
              )}
            </Section>

            <Section title="Interaction Partners" note="Countries this one interacts with most">
              {detail.partners.length > 0 ? (
                <div className="max-h-[420px] overflow-y-auto">
                  <table className="w-full text-xs">
                    <thead className="sticky top-0" style={{ background: '#111114' }}>
                      <tr className="text-left text-[10px] font-mono text-zinc-600 uppercase">
                        <th className="py-2 pr-3 font-medium">Partner</th>
                        <th className="py-2 pr-3 font-medium text-right">Events</th>
                        <th className="py-2 pr-3 font-medium text-right">Goldstein</th>
                        <th className="py-2 font-medium text-right">Tone</th>
                      </tr>
                    </thead>
                    <tbody>
                      {detail.partners.map(p => (
                        <tr key={p.partner} className="border-t border-zinc-800/40 text-zinc-400">
                          <td className="py-2 pr-3 font-mono">
                            <CodeButton code={p.partner} active={country} onSelect={setCountry} />
                          </td>
                          <td className="py-2 pr-3 text-right font-mono">{p.event_count.toLocaleString()}</td>
                          <td className="py-2 pr-3 text-right font-mono" style={{ color: goldsteinColor(p.avg_goldstein) }}>
                            {p.avg_goldstein.toFixed(2)}
                          </td>
                          <td className="py-2 text-right font-mono" style={{ color: toneColor(p.avg_tone) }}>
                            {p.avg_tone.toFixed(2)}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              ) : (
                <EmptyState text="No partners in this range" />
              )}
            </Section>
          </div>

          <Section title="Biggest Stories" note={`Top ${detail.top_events.length} most-mentioned events involving ${detail.country} (one row per article)`}>
            {detail.top_events.length > 0 ? (
              <div className="max-h-[440px] overflow-y-auto">
                <table className="w-full text-xs">
                  <thead className="sticky top-0" style={{ background: '#111114' }}>
                    <tr className="text-left text-[10px] font-mono text-zinc-600 uppercase">
                      <th className="py-2 pr-3 font-medium">Reported</th>
                      <th className="py-2 pr-3 font-medium">Actors</th>
                      <th className="py-2 pr-3 font-medium">Event</th>
                      <th className="py-2 pr-3 font-medium text-right">Goldstein</th>
                      <th className="py-2 pr-3 font-medium text-right">Mentions</th>
                      <th className="py-2 pr-3 font-medium text-right">Sources</th>
                      <th className="py-2 font-medium">Link</th>
                    </tr>
                  </thead>
                  <tbody>
                    {detail.top_events.map((e, i) => (
                      <tr key={`${e.source_url}-${i}`} className="border-t border-zinc-800/40 text-zinc-400">
                        <td className="py-2 pr-3 font-mono text-[11px] whitespace-nowrap">{e.ingest_date}</td>
                        <td className="py-2 pr-3 max-w-[220px] truncate" title={`${e.actor1} → ${e.actor2}`}>
                          {e.actor1 || '—'} <span className="text-zinc-600">→</span> {e.actor2 || '—'}
                        </td>
                        <td className="py-2 pr-3 max-w-[200px] truncate" title={`${e.event_code} ${cameoFullLabel(e.event_code)}`}>
                          {cameoFullLabel(e.event_code)}
                        </td>
                        <td className="py-2 pr-3 text-right font-mono" style={{ color: goldsteinColor(e.goldstein) }}>
                          {e.goldstein.toFixed(1)}
                        </td>
                        <td className="py-2 pr-3 text-right font-mono">{e.mention_count.toLocaleString()}</td>
                        <td className="py-2 pr-3 text-right font-mono">{e.source_count.toLocaleString()}</td>
                        <td className="py-2 max-w-[200px]"><SourceLink raw={e.source_url} /></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <EmptyState text="No events with source links in this range" />
            )}
          </Section>
        </>
      )}
    </div>
  );
}

// Country codes double as drill-down buttons wherever they appear.
function CodeButton({ code, active, onSelect }: {
  code: string; active: string; onSelect: (c: string) => void;
}) {
  return (
    <button
      onClick={() => onSelect(code)}
      className={`cursor-pointer transition-colors ${
        code === active ? 'text-cyan-400' : 'text-zinc-300 hover:text-cyan-300'
      }`}
    >
      {code}
    </button>
  );
}

function countryDailyOption(daily: GdeltCountryDaily[]) {
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
      { type: 'value', name: 'score', nameTextStyle: AXIS_LABEL, axisLabel: AXIS_LABEL, splitLine: { show: false } },
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
      {
        name: 'Goldstein',
        type: 'line',
        yAxisIndex: 1,
        data: daily.map(d => d.avg_goldstein),
        smooth: true,
        showSymbol: daily.length <= 31,
        lineStyle: { color: '#a78bfa', width: 2 },
        itemStyle: { color: '#a78bfa' },
      },
    ],
  };
}

interface TypeBarParam { data: { value: number; code: string; goldstein: number }; name: string }

function eventTypesOption(types: GdeltCountryEventType[]) {
  const sorted = [...types].slice(0, 15).sort((a, b) => a.event_count - b.event_count);
  return {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      ...CHART_TOOLTIP,
      formatter: (params: TypeBarParam[]) => {
        const p = params[0];
        return `<div style="font-weight:600;color:#a1a1aa;font-size:11px;margin-bottom:4px">${p.name} (${p.data.code})</div>
                <div style="color:#f4f4f5;font-size:13px;font-weight:600">${Number(p.data.value).toLocaleString()} events</div>
                <div style="color:#a1a1aa;font-size:11px">Goldstein ${p.data.goldstein}</div>`;
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
      data: sorted.map(t => cameoFullLabel(t.event_code)),
      axisLabel: { ...AXIS_LABEL, fontSize: 10, width: 170, overflow: 'truncate' },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [{
      type: 'bar',
      data: sorted.map(t => ({
        value: t.event_count,
        code: t.event_code,
        goldstein: t.avg_goldstein,
        itemStyle: {
          color: t.avg_goldstein < 0 ? '#dc2626' : '#0284c7',
          opacity: 0.85,
          borderRadius: [0, 4, 4, 0],
        },
      })),
      barMaxWidth: 14,
      label: {
        show: true, position: 'right', color: '#a1a1aa', fontSize: 9,
        fontFamily: 'JetBrains Mono, monospace',
        formatter: (p: { value: number }) => Number(p.value).toLocaleString(),
      },
    }],
  };
}

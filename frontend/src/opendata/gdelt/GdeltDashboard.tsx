import { useState } from 'react';
import type React from 'react';
import { ErrorBanner } from '../../dashboards/shared';
import { MAX_EVENTS_DAYS, daysAgoUTC, spanOf, DateInput } from './shared';
import OverviewTab from './OverviewTab';
import CountryTab from './CountryTab';
import ImpactTab from './ImpactTab';
import StoriesTab from './StoriesTab';
import IndustryTab from './IndustryTab';

const TABS = [
  { id: 'overview', label: 'Overview' },
  { id: 'country', label: 'Country & Relations' },
  { id: 'impact', label: 'Human Impact' },
  { id: 'stories', label: 'Story Velocity' },
  { id: 'industry', label: 'Industry Pulse' },
] as const;

type TabId = (typeof TABS)[number]['id'];

const PRESETS = [
  { label: '3 days', days: 3 },
  { label: '7 days', days: 7 },
  { label: '30 days', days: 30 },
];

// Shell: owns the UTC date range shared by every tab; tabs mount lazily on
// first visit and stay mounted (hidden) so switching back is instant.
export default function GdeltDashboard() {
  const [startDate, setStartDate] = useState(daysAgoUTC(2));
  const [endDate, setEndDate] = useState(daysAgoUTC(0));
  const [active, setActive] = useState<TabId>('overview');
  const [visited, setVisited] = useState<ReadonlySet<TabId>>(new Set<TabId>(['overview']));

  const today = daysAgoUTC(0);
  const span = spanOf(startDate, endDate);
  const rangeError = span < 1
    ? 'Start date must be on or before end date.'
    : span > MAX_EVENTS_DAYS
      ? `Date range spans ${span} days; at most ${MAX_EVENTS_DAYS} days are supported.`
      : '';

  const select = (id: TabId) => {
    setActive(id);
    setVisited(prev => new Set(prev).add(id));
  };

  function setPreset(days: number) {
    setStartDate(daysAgoUTC(days - 1));
    setEndDate(daysAgoUTC(0));
  }

  const tabBody: Record<TabId, React.ReactNode> = {
    overview: <OverviewTab startDate={startDate} endDate={endDate} />,
    country: <CountryTab startDate={startDate} endDate={endDate} />,
    impact: <ImpactTab startDate={startDate} endDate={endDate} />,
    stories: <StoriesTab startDate={startDate} endDate={endDate} />,
    industry: <IndustryTab startDate={startDate} endDate={endDate} />,
  };

  return (
    <div className="space-y-6">
      {/* Filter bar: quick presets + custom UTC date range */}
      <div className="rounded-2xl border border-zinc-800/50 p-4 flex flex-wrap items-end gap-4" style={{ background: '#111114' }}>
        <div>
          <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">Quick Range</label>
          <div className="flex gap-1.5">
            {PRESETS.map(p => {
              const activePreset = startDate === daysAgoUTC(p.days - 1) && endDate === today;
              return (
                <button
                  key={p.days}
                  onClick={() => setPreset(p.days)}
                  className={`text-xs rounded-lg px-3 py-2 border cursor-pointer transition-colors ${
                    activePreset
                      ? 'border-cyan-500/30 bg-cyan-500/5 text-cyan-400'
                      : 'border-zinc-800/50 text-zinc-400 hover:border-zinc-700/60'
                  }`}
                  style={{ background: activePreset ? undefined : '#09090b' }}
                >
                  {p.label}
                </button>
              );
            })}
          </div>
        </div>
        <DateInput label="From" value={startDate} max={endDate <= today ? endDate : today} onChange={setStartDate} />
        <DateInput label="To" value={endDate} min={startDate} max={today} onChange={setEndDate} />
        <p className="text-[10px] text-zinc-600 ml-auto self-center max-w-[260px] leading-relaxed">
          Dates reported in UTC. GDELT refreshes every 15 minutes; events support {MAX_EVENTS_DAYS} days,
          heavier tabs cap tighter and say so.
        </p>
      </div>

      <div className="flex items-center gap-1 border-b border-zinc-800/60 pb-2">
        {TABS.map(t => (
          <button
            key={t.id}
            onClick={() => select(t.id)}
            className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors ${
              active === t.id ? 'bg-zinc-800 text-white' : 'text-zinc-500 hover:text-zinc-300'
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>

      {rangeError && <ErrorBanner message={rangeError} />}
      {!rangeError && TABS.map(t =>
        visited.has(t.id) ? (
          <div key={t.id} className={active === t.id ? '' : 'hidden'}>
            {tabBody[t.id]}
          </div>
        ) : null,
      )}
    </div>
  );
}

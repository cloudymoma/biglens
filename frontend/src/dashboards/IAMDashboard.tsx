import React, { useState, useEffect, useRef, useCallback } from 'react';
import ReactECharts from 'echarts-for-react';
import {
  Shield, Users, Bot, Activity, Search, X,
  AlertTriangle, Clock, Download,
} from 'lucide-react';
import type { IAMDashboardData, InactiveEmail, NewActor, OffHoursCell, OffHoursUser, ExfilSignal } from '../types';
import { fetchIAMDashboard, fetchEmailSuggestions } from '../api';
import { formatBytes, MetricCard, EmptyState, ErrorBanner } from './shared';
import SecurityPosture from './SecurityPosture';

interface Props {
  region: string;
  timeRange: string;
}

export default function IAMDashboard({ region, timeRange }: Props) {
  const [data, setData] = useState<IAMDashboardData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [selectedEmails, setSelectedEmails] = useState<string[]>([]);
  const [view, setView] = useState<'activity' | 'posture'>('activity');

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchIAMDashboard(region, selectedEmails, timeRange)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [region, timeRange, selectedEmails]);

  const handleAddEmail = (email: string) => {
    if (!selectedEmails.includes(email)) {
      setSelectedEmails(prev => [...prev, email]);
    }
  };

  const handleRemoveEmail = (email: string) => {
    setSelectedEmails(prev => prev.filter(e => e !== email));
  };

  return (
    <div className="space-y-6">
      {/* Pill toggle */}
      <div className="flex items-center rounded-lg border border-zinc-800/50 overflow-hidden w-fit" style={{ background: '#09090b' }}>
        <button
          onClick={() => setView('activity')}
          className={`px-4 py-2 text-xs font-medium cursor-pointer transition-all ${
            view === 'activity' ? 'text-white' : 'text-zinc-600 hover:text-zinc-400'
          }`}
          style={view === 'activity' ? { background: '#14532d20' } : undefined}
        >
          Activity & Anomalies
        </button>
        <button
          onClick={() => setView('posture')}
          className={`px-4 py-2 text-xs font-medium cursor-pointer transition-all ${
            view === 'posture' ? 'text-white' : 'text-zinc-600 hover:text-zinc-400'
          }`}
          style={view === 'posture' ? { background: '#14532d20' } : undefined}
        >
          Access Posture
        </button>
      </div>

      {view === 'posture' ? (
        <SecurityPosture region={region} timeRange={timeRange} />
      ) : (
        <>
          <EmailSearchBar
            region={region}
            selectedEmails={selectedEmails}
            onAdd={handleAddEmail}
            onRemove={handleRemoveEmail}
            onClear={() => setSelectedEmails([])}
          />

          {loading && <LoadingPulse />}
          {error && <ErrorBanner message={error} />}
          {!loading && !error && data && (
            <>
              <SummaryCards summary={data.summary} />
              <UsageTimelineChart timeline={data.timeline || []} />
              <TopCallersTable callers={data.top_callers || []} />
              <InactiveSection
                inactive7={data.inactive_7d || []}
                inactive30={data.inactive_30d || []}
                inactive90={data.inactive_90d || []}
              />
              <NewActorsCard actors={data.new_actors || []} />
              <OffHoursHeatmap cells={data.off_hours || []} top={data.off_hours_top || []} />
              <ExfilSignalsTable signals={data.exfil_signals || []} />
            </>
          )}
        </>
      )}
    </div>
  );
}

// --- Email Search Bar with autocomplete ---

function EmailSearchBar({ region, selectedEmails, onAdd, onRemove, onClear }: {
  region: string;
  selectedEmails: string[];
  onAdd: (email: string) => void;
  onRemove: (email: string) => void;
  onClear: () => void;
}) {
  const [query, setQuery] = useState('');
  const [suggestions, setSuggestions] = useState<string[]>([]);
  const [showDropdown, setShowDropdown] = useState(false);
  const [activeIndex, setActiveIndex] = useState(-1);
  const ref = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined);

  const fetchSuggestions = useCallback((q: string) => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    if (q.length < 1) {
      setSuggestions([]);
      return;
    }
    debounceRef.current = setTimeout(() => {
      fetchEmailSuggestions(region, q).then(results => {
        setSuggestions(results.filter(e => !selectedEmails.includes(e)));
        setActiveIndex(-1);
      });
    }, 200);
  }, [region, selectedEmails]);

  useEffect(() => {
    fetchSuggestions(query);
  }, [query, fetchSuggestions]);

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setShowDropdown(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, []);

  const handleSelect = (email: string) => {
    onAdd(email);
    setQuery('');
    setSuggestions([]);
    setShowDropdown(false);
    inputRef.current?.focus();
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActiveIndex(i => Math.min(i + 1, suggestions.length - 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActiveIndex(i => Math.max(i - 1, 0));
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (activeIndex >= 0 && activeIndex < suggestions.length) {
        handleSelect(suggestions[activeIndex]);
      } else if (query.includes('@')) {
        handleSelect(query);
      }
    } else if (e.key === 'Backspace' && query === '' && selectedEmails.length > 0) {
      onRemove(selectedEmails[selectedEmails.length - 1]);
    } else if (e.key === 'Escape') {
      setShowDropdown(false);
    }
  };

  return (
    <div ref={ref} className="relative">
      <div
        className="rounded-2xl border border-zinc-800/50 p-4 transition-all focus-within:border-cyan-500/30"
        style={{ background: '#111114' }}
      >
        <div className="flex items-center gap-2 mb-3">
          <Search size={14} className="text-cyan-400" />
          <span className="text-xs font-semibold text-zinc-400 uppercase tracking-wider">Filter by Identity</span>
          {selectedEmails.length > 0 && (
            <button
              onClick={onClear}
              className="ml-auto text-[10px] text-zinc-600 hover:text-zinc-400 cursor-pointer transition-colors"
            >
              Clear all
            </button>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {selectedEmails.map(email => (
            <span
              key={email}
              className="inline-flex items-center gap-1.5 pl-2.5 pr-1.5 py-1 rounded-lg text-xs font-mono border transition-colors"
              style={{
                background: email.includes('gserviceaccount.com') ? '#7c3aed10' : '#0e749015',
                borderColor: email.includes('gserviceaccount.com') ? '#7c3aed30' : '#0e749030',
                color: email.includes('gserviceaccount.com') ? '#c084fc' : '#38bdf8',
              }}
            >
              {email.includes('gserviceaccount.com') ? <Bot size={11} /> : <Users size={11} />}
              <span className="max-w-[200px] truncate">{email}</span>
              <button
                onClick={() => onRemove(email)}
                className="p-0.5 rounded hover:bg-zinc-800/60 cursor-pointer transition-colors"
              >
                <X size={11} />
              </button>
            </span>
          ))}

          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={e => { setQuery(e.target.value); setShowDropdown(true); }}
            onFocus={() => setShowDropdown(true)}
            onKeyDown={handleKeyDown}
            placeholder={selectedEmails.length > 0 ? 'Add more...' : 'Search users or service accounts...'}
            className="flex-1 min-w-[200px] text-xs text-zinc-300 bg-transparent outline-none placeholder:text-zinc-700 font-mono"
          />
        </div>
      </div>

      {showDropdown && suggestions.length > 0 && (
        <div
          className="absolute z-50 mt-1 w-full rounded-xl border border-zinc-800/60 shadow-2xl overflow-hidden"
          style={{ background: '#111114' }}
        >
          <div className="max-h-64 overflow-y-auto">
            {suggestions.map((email, i) => (
              <button
                key={email}
                onClick={() => handleSelect(email)}
                className={`w-full text-left px-4 py-2.5 text-xs font-mono cursor-pointer transition-colors flex items-center gap-2.5 ${
                  i === activeIndex
                    ? 'bg-cyan-500/10 text-cyan-400'
                    : 'text-zinc-400 hover:bg-zinc-800/50 hover:text-zinc-200'
                }`}
              >
                {email.includes('gserviceaccount.com')
                  ? <Bot size={13} className="text-purple-400 shrink-0" />
                  : <Users size={13} className="text-cyan-400 shrink-0" />
                }
                <span className="truncate">{email}</span>
                <span className="ml-auto text-[10px] text-zinc-600 shrink-0">
                  {email.includes('gserviceaccount.com') ? 'Service Account' : 'User'}
                </span>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

// --- Summary KPI Cards ---

function SummaryCards({ summary }: { summary: IAMDashboardData['summary'] }) {
  return (
    <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
      <MetricCard
        label="Total Identities"
        value={summary?.total_emails?.toString() || '0'}
        icon={<Shield size={18} />}
        detail="Unique emails in period"
        accentColor="#38bdf8"
      />
      <MetricCard
        label="Human Users"
        value={summary?.human_users?.toString() || '0'}
        icon={<Users size={18} />}
        detail="Personal accounts"
        accentColor="#4ade80"
      />
      <MetricCard
        label="Service Accounts"
        value={summary?.service_accounts?.toString() || '0'}
        icon={<Bot size={18} />}
        detail="*.gserviceaccount.com"
        accentColor="#c084fc"
      />
      <MetricCard
        label="Total API Calls"
        value={formatNumber(summary?.total_calls || 0)}
        icon={<Activity size={18} />}
        detail="Jobs executed"
        accentColor="#fbbf24"
      />
    </div>
  );
}

// --- Usage Timeline Chart ---

function UsageTimelineChart({ timeline }: { timeline: IAMDashboardData['timeline'] & {} }) {
  if (!timeline || timeline.length === 0) {
    return (
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">API Usage Over Time</h3>
        <p className="text-xs text-zinc-500 mb-4">Jobs per interval by identity</p>
        <EmptyState text="No usage data available" />
      </div>
    );
  }

  const emails = [...new Set(timeline.map(t => t.email))];
  const buckets = [...new Set(timeline.map(t => t.bucket))].sort();

  const COLORS = [
    '#38bdf8', '#c084fc', '#4ade80', '#fb7185', '#fbbf24',
    '#60a5fa', '#a78bfa', '#34d399', '#f472b6', '#f59e0b',
  ];

  const emailMap = new Map<string, Map<string, number>>();
  for (const e of emails) emailMap.set(e, new Map());
  for (const t of timeline) emailMap.get(t.email)!.set(t.bucket, t.call_count);

  const series = emails.map((email, i) => ({
    name: email.split('@')[0],
    type: 'line' as const,
    smooth: true,
    showSymbol: false,
    lineStyle: { width: 2, color: COLORS[i % COLORS.length] },
    areaStyle: {
      color: {
        type: 'linear' as const, x: 0, y: 0, x2: 0, y2: 1,
        colorStops: [
          { offset: 0, color: COLORS[i % COLORS.length] + '30' },
          { offset: 1, color: COLORS[i % COLORS.length] + '00' },
        ],
      },
    },
    data: buckets.map(b => emailMap.get(email)!.get(b) || 0),
    stack: emails.length > 5 ? 'total' : undefined,
  }));

  const option = {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: 'rgba(17,17,20,0.95)',
      borderColor: '#27272a',
      textStyle: { color: '#e4e4e7', fontSize: 11 },
      axisPointer: { type: 'cross', crossStyle: { color: '#3f3f46' } },
    },
    legend: {
      bottom: 0,
      textStyle: { color: '#71717a', fontSize: 10 },
      itemWidth: 12, itemHeight: 3,
      type: 'scroll' as const,
    },
    grid: { left: 52, right: 24, bottom: 40, top: 20 },
    xAxis: {
      type: 'category' as const,
      data: buckets.map(b => {
        const d = new Date(b);
        return buckets.length > 48
          ? `${(d.getMonth()+1).toString().padStart(2,'0')}/${d.getDate().toString().padStart(2,'0')}`
          : `${d.getHours().toString().padStart(2,'0')}:${d.getMinutes().toString().padStart(2,'0')}`;
      }),
      axisLabel: {
        color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace',
        interval: Math.max(Math.floor(buckets.length / 12), 1),
      },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    yAxis: {
      type: 'value' as const,
      axisLabel: {
        color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace',
        formatter: (v: number) => v >= 1e3 ? `${(v/1e3).toFixed(0)}k` : v.toString(),
      },
      splitLine: { lineStyle: { color: '#1f1f23', type: 'dashed' as const } },
    },
    series,
    dataZoom: [{ type: 'inside', start: 0, end: 100 }],
  };

  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <h3 className="text-sm font-semibold text-white mb-1">API Usage Over Time</h3>
      <p className="text-xs text-zinc-500 mb-4">Jobs per interval by identity</p>
      <div className="h-[360px]">
        <ReactECharts option={option} style={{ height: '100%' }} />
      </div>
    </div>
  );
}

// --- Top Callers Table ---

function TopCallersTable({ callers }: { callers: IAMDashboardData['top_callers'] & {} }) {
  if (!callers || callers.length === 0) {
    return (
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Most Active Identities</h3>
        <p className="text-xs text-zinc-500 mb-4">Ranked by total API calls</p>
        <EmptyState text="No caller data" />
      </div>
    );
  }

  const maxCalls = callers[0]?.total_calls || 1;

  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <div className="flex items-center justify-between mb-4">
        <div>
          <h3 className="text-sm font-semibold text-white mb-1">Most Active Identities</h3>
          <p className="text-xs text-zinc-500">Ranked by total API calls</p>
        </div>
        <div className="flex items-center gap-3 text-[10px] text-zinc-600">
          <span className="flex items-center gap-1"><span className="w-2 h-2 rounded-full bg-cyan-400 inline-block" /> User</span>
          <span className="flex items-center gap-1"><span className="w-2 h-2 rounded-full bg-purple-400 inline-block" /> Service Acct</span>
        </div>
      </div>

      <div className="overflow-x-auto">
        <table className="w-full text-xs">
          <thead>
            <tr className="text-zinc-500 border-b border-zinc-800/50">
              <th className="text-left py-2.5 px-3 font-medium w-8">#</th>
              <th className="text-left py-2.5 px-3 font-medium">Identity</th>
              <th className="text-right py-2.5 px-3 font-medium">Calls</th>
              <th className="text-right py-2.5 px-3 font-medium">Slot Time</th>
              <th className="text-right py-2.5 px-3 font-medium">Bytes Billed</th>
              <th className="text-right py-2.5 px-3 font-medium">Avg Duration</th>
              <th className="text-right py-2.5 px-3 font-medium">Last Active</th>
              <th className="text-right py-2.5 px-3 font-medium w-36">Volume</th>
            </tr>
          </thead>
          <tbody>
            {callers.map((c, i) => {
              const isSA = c.email.includes('gserviceaccount.com');
              const pct = (c.total_calls / maxCalls) * 100;
              return (
                <tr key={c.email} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                  <td className="py-3 px-3 text-zinc-600 font-mono">{i + 1}</td>
                  <td className="py-3 px-3">
                    <div className="flex items-center gap-2">
                      {isSA
                        ? <Bot size={13} className="text-purple-400 shrink-0" />
                        : <Users size={13} className="text-cyan-400 shrink-0" />
                      }
                      <span className="font-mono text-white truncate max-w-[260px]" title={c.email}>{c.email}</span>
                    </div>
                  </td>
                  <td className="py-3 px-3 text-right font-mono text-zinc-300">{formatNumber(c.total_calls)}</td>
                  <td className="py-3 px-3 text-right font-mono text-zinc-400">{formatSlotTime(c.total_slot_ms)}</td>
                  <td className="py-3 px-3 text-right font-mono text-zinc-400">{formatBytes(c.total_bytes)}</td>
                  <td className="py-3 px-3 text-right font-mono text-zinc-400">{c.avg_duration_sec.toFixed(1)}s</td>
                  <td className="py-3 px-3 text-right font-mono text-zinc-500">{formatRelativeTime(c.last_active)}</td>
                  <td className="py-3 px-3">
                    <div className="w-full h-1.5 rounded-full bg-zinc-800">
                      <div
                        className="h-full rounded-full transition-all"
                        style={{
                          width: `${pct}%`,
                          background: isSA
                            ? 'linear-gradient(90deg, #7c3aed, #c084fc)'
                            : 'linear-gradient(90deg, #0e7490, #38bdf8)',
                        }}
                      />
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// --- Inactive Emails Section with tabbed 7/30/90d ---

function InactiveSection({ inactive7, inactive30, inactive90 }: {
  inactive7: InactiveEmail[];
  inactive30: InactiveEmail[];
  inactive90: InactiveEmail[];
}) {
  const [activeTab, setActiveTab] = useState<'7d' | '30d' | '90d'>('30d');

  const tabs: { id: '7d' | '30d' | '90d'; label: string; data: InactiveEmail[]; severity: string }[] = [
    { id: '7d', label: '7 Days', data: inactive7, severity: 'low' },
    { id: '30d', label: '30 Days', data: inactive30, severity: 'medium' },
    { id: '90d', label: '90 Days', data: inactive90, severity: 'high' },
  ];

  const currentTab = tabs.find(t => t.id === activeTab)!;

  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <div className="flex items-center justify-between mb-4">
        <div>
          <div className="flex items-center gap-2 mb-1">
            <AlertTriangle size={15} className="text-amber-400" />
            <h3 className="text-sm font-semibold text-white">Inactive Identities</h3>
          </div>
          <p className="text-xs text-zinc-500">Identities with no activity — candidates for review or deactivation</p>
        </div>

        <div className="flex items-center rounded-lg border border-zinc-800/50 overflow-hidden" style={{ background: '#09090b' }}>
          {tabs.map(tab => (
            <button
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={`px-3.5 py-1.5 text-xs font-medium cursor-pointer transition-all ${
                activeTab === tab.id
                  ? 'text-white'
                  : 'text-zinc-600 hover:text-zinc-400'
              }`}
              style={activeTab === tab.id ? {
                background: tab.severity === 'high' ? '#991b1b20' : tab.severity === 'medium' ? '#92400e20' : '#14532d20',
              } : undefined}
            >
              {tab.label}
              <span className={`ml-1.5 text-[10px] px-1.5 py-0.5 rounded-full font-mono ${
                activeTab === tab.id
                  ? tab.severity === 'high' ? 'bg-red-500/20 text-red-400'
                    : tab.severity === 'medium' ? 'bg-amber-500/20 text-amber-400'
                    : 'bg-emerald-500/20 text-emerald-400'
                  : 'bg-zinc-800 text-zinc-600'
              }`}>
                {tab.data.length}
              </span>
            </button>
          ))}
        </div>
      </div>

      {currentTab.data.length === 0 ? (
        <EmptyState text={`No identities inactive for ${currentTab.label.toLowerCase()}`} />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="text-zinc-500 border-b border-zinc-800/50">
                <th className="text-left py-2.5 px-3 font-medium">Identity</th>
                <th className="text-left py-2.5 px-3 font-medium">Type</th>
                <th className="text-right py-2.5 px-3 font-medium">Days Idle</th>
                <th className="text-right py-2.5 px-3 font-medium">Last Active</th>
                <th className="text-right py-2.5 px-3 font-medium">Historical Calls</th>
                <th className="text-right py-2.5 px-3 font-medium">Risk</th>
              </tr>
            </thead>
            <tbody>
              {currentTab.data.map(item => {
                const isSA = item.email.includes('gserviceaccount.com');
                const risk = item.days_idle >= 90 ? 'high' : item.days_idle >= 30 ? 'medium' : 'low';
                return (
                  <tr key={item.email} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                    <td className="py-3 px-3">
                      <div className="flex items-center gap-2">
                        {isSA
                          ? <Bot size={13} className="text-purple-400 shrink-0" />
                          : <Users size={13} className="text-cyan-400 shrink-0" />
                        }
                        <span className="font-mono text-white truncate max-w-[300px]" title={item.email}>{item.email}</span>
                      </div>
                    </td>
                    <td className="py-3 px-3">
                      <span className={`text-[10px] px-2 py-0.5 rounded-full font-medium ${
                        isSA ? 'bg-purple-500/10 text-purple-400 border border-purple-500/20'
                             : 'bg-cyan-500/10 text-cyan-400 border border-cyan-500/20'
                      }`}>
                        {isSA ? 'Service Acct' : 'User'}
                      </span>
                    </td>
                    <td className="py-3 px-3 text-right font-mono text-zinc-300">{item.days_idle}d</td>
                    <td className="py-3 px-3 text-right font-mono text-zinc-500">{formatRelativeTime(item.last_active)}</td>
                    <td className="py-3 px-3 text-right font-mono text-zinc-400">{formatNumber(item.total_calls)}</td>
                    <td className="py-3 px-3 text-right">
                      <span className={`text-[10px] px-2 py-0.5 rounded-full font-semibold ${
                        risk === 'high' ? 'bg-red-500/15 text-red-400 border border-red-500/20'
                          : risk === 'medium' ? 'bg-amber-500/15 text-amber-400 border border-amber-500/20'
                          : 'bg-emerald-500/15 text-emerald-400 border border-emerald-500/20'
                      }`}>
                        {risk === 'high' ? 'High' : risk === 'medium' ? 'Medium' : 'Low'}
                      </span>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// --- Helpers ---

function formatNumber(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return n.toLocaleString();
}

function formatSlotTime(ms: number): string {
  if (ms <= 0) return '0s';
  const sec = ms / 1000;
  if (sec >= 3600) return `${(sec / 3600).toFixed(1)}h`;
  if (sec >= 60) return `${(sec / 60).toFixed(1)}m`;
  return `${sec.toFixed(0)}s`;
}

function formatRelativeTime(iso: string): string {
  if (!iso) return 'N/A';
  const diff = Date.now() - new Date(iso).getTime();
  const days = Math.floor(diff / 86400000);
  if (days === 0) return 'Today';
  if (days === 1) return 'Yesterday';
  if (days < 7) return `${days}d ago`;
  if (days < 30) return `${Math.floor(days / 7)}w ago`;
  return `${Math.floor(days / 30)}mo ago`;
}

// --- New Actors Card ---

function NewActorsCard({ actors }: { actors: NewActor[] }) {
  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <h3 className="text-sm font-semibold text-white mb-1">New Actors (first seen ≤ 7d)</h3>
      <p className="text-xs text-zinc-500 mb-4">
        Principals whose first job in the 90-day baseline is recent — new service accounts are the classic credential-misuse tell (idle &gt;90d re-appears as new)
      </p>

      {actors.length > 0 ? (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="text-zinc-500 border-b border-zinc-800/50">
                <th className="text-left py-2.5 px-3 font-medium">Identity</th>
                <th className="text-right py-2.5 px-3 font-medium">First Seen</th>
                <th className="text-right py-2.5 px-3 font-medium">Jobs</th>
              </tr>
            </thead>
            <tbody>
              {actors.map((actor, i) => (
                <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                  <td className="py-3 px-3">
                    <div className="flex items-center gap-2">
                      {actor.is_sa ? <Bot size={13} className="text-purple-400 shrink-0" /> : <Users size={13} className="text-cyan-400 shrink-0" />}
                      <span className="font-mono text-white truncate max-w-[260px]" title={actor.email}>{actor.email}</span>
                      {actor.is_sa && (
                        <span className="text-[10px] px-2 py-0.5 rounded-full font-medium border bg-amber-500/10 text-amber-400 border-amber-500/20">
                          SERVICE ACCT
                        </span>
                      )}
                    </div>
                  </td>
                  <td className="py-3 px-3 text-right font-mono text-zinc-400">{formatRelativeTime(actor.first_seen)}</td>
                  <td className="py-3 px-3 text-right font-mono text-zinc-300">{formatNumber(actor.jobs)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <EmptyState text="No new principals in the last 7 days" />
      )}
    </div>
  );
}

// --- Off-Hours Heatmap ---

function OffHoursHeatmap({ cells, top }: { cells: OffHoursCell[]; top: OffHoursUser[] }) {
  const DOW_LABELS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

  const heatmapData = cells.map(c => [c.hr, c.dow - 1, c.jobs]);
  const maxJobs = cells.length > 0 ? Math.max(...cells.map(c => c.jobs)) : 1;

  const option = {
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'item',
      backgroundColor: 'rgba(17,17,20,0.95)',
      borderColor: '#27272a',
      textStyle: { color: '#e4e4e7', fontSize: 11 },
      formatter: (params: any) => {
        const [hr, dow, jobs] = params.data;
        return `${DOW_LABELS[dow]} ${hr.toString().padStart(2, '0')}:00 UTC<br/>${jobs} jobs`;
      },
    },
    grid: { left: 48, right: 24, bottom: 32, top: 12 },
    xAxis: {
      type: 'category',
      data: Array.from({ length: 24 }, (_, i) => i.toString().padStart(2, '0')),
      axisLabel: { color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' },
      axisLine: { show: false },
      axisTick: { show: false },
      splitArea: { show: false },
    },
    yAxis: {
      type: 'category',
      data: DOW_LABELS,
      axisLabel: { color: '#71717a', fontSize: 10, fontFamily: 'JetBrains Mono, monospace' },
      axisLine: { show: false },
      axisTick: { show: false },
      splitArea: { show: false },
    },
    visualMap: {
      show: false,
      min: 0,
      max: maxJobs,
      inRange: { color: ['#111114', '#0e7490', '#38bdf8'] },
    },
    series: [{
      type: 'heatmap',
      data: heatmapData,
      itemStyle: { borderColor: '#09090b', borderWidth: 1 },
      emphasis: {
        itemStyle: { shadowBlur: 4, shadowColor: 'rgba(56,189,248,0.5)' },
      },
    }],
  };

  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <div className="flex items-center gap-2 mb-1">
        <Clock size={16} className="text-cyan-400" />
        <h3 className="text-sm font-semibold text-white">Off-Hours Activity</h3>
      </div>
      <p className="text-xs text-zinc-500 mb-4">Human (non-service-account) jobs by weekday × hour, UTC</p>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <div className="lg:col-span-2">
          {cells.length > 0 ? (
            <div className="h-[280px]">
              <ReactECharts option={option} style={{ height: '100%' }} />
            </div>
          ) : (
            <EmptyState text="No off-hours activity data" />
          )}
        </div>

        <div>
          <h4 className="text-xs font-semibold text-white mb-3">Top Off-Hours Principals (00:00–06:00 UTC)</h4>
          {top.length > 0 ? (
            <div className="space-y-2">
              {top.slice(0, 10).map((user, i) => (
                <div key={i} className="flex items-center justify-between text-xs">
                  <span className="font-mono text-zinc-400 truncate max-w-[180px]" title={user.email}>{user.email}</span>
                  <span className="font-mono text-zinc-300 shrink-0">{formatNumber(user.jobs)}</span>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-xs text-zinc-500 italic">No off-hours activity</p>
          )}
        </div>
      </div>
    </div>
  );
}

// --- Exfil Signals Table ---

function ExfilSignalsTable({ signals }: { signals: ExfilSignal[] }) {
  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <div className="flex items-center gap-2 mb-1">
        <Download size={16} className="text-rose-400" />
        <h3 className="text-sm font-semibold text-white">Exfiltration Signals</h3>
      </div>
      <p className="text-xs text-zinc-500 mb-4">
        Extracts, EXPORT DATA, cross-project writes, and &gt;1 TiB scans — not visible: Storage Read API reads, RLS-blanked bytes
      </p>

      {signals.length > 0 ? (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="text-zinc-500 border-b border-zinc-800/50">
                <th className="text-left py-2.5 px-3 font-medium">User</th>
                <th className="text-left py-2.5 px-3 font-medium">Signal</th>
                <th className="text-right py-2.5 px-3 font-medium">Bytes</th>
                <th className="text-left py-2.5 px-3 font-medium">Dest Project</th>
                <th className="text-right py-2.5 px-3 font-medium">Created</th>
                <th className="text-left py-2.5 px-3 font-medium">Job ID</th>
              </tr>
            </thead>
            <tbody>
              {signals.map((sig, i) => (
                <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                  <td className="py-3 px-3 text-white font-mono max-w-[200px] truncate" title={sig.email}>{sig.email}</td>
                  <td className="py-3 px-3">
                    <span
                      className={`text-[10px] px-2 py-0.5 rounded-full font-medium border ${
                        sig.signal === 'EXTRACT_TO_GCS' || sig.signal === 'EXPORT_DATA'
                          ? 'bg-rose-500/10 text-rose-400 border-rose-500/20'
                          : sig.signal === 'CROSS_PROJECT_WRITE'
                          ? 'bg-amber-500/10 text-amber-400 border-amber-500/20'
                          : 'bg-zinc-500/10 text-zinc-400 border-zinc-500/20'
                      }`}
                    >
                      {sig.signal}
                    </span>
                  </td>
                  <td className="py-3 px-3 text-right font-mono text-zinc-300">{formatBytes(sig.bytes)}</td>
                  <td className="py-3 px-3 text-zinc-400 font-mono">{sig.dest_project || '—'}</td>
                  <td className="py-3 px-3 text-right font-mono text-zinc-500">{formatRelativeTime(sig.created)}</td>
                  <td className="py-3 px-3 text-zinc-400 font-mono max-w-[160px] truncate" title={sig.job_id}>{sig.job_id}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <EmptyState text="No exfiltration signals detected" />
      )}
    </div>
  );
}

// --- Helpers ---

function LoadingPulse() {
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
        {[1, 2, 3, 4].map(i => (
          <div key={i} className="h-28 rounded-2xl animate-pulse" style={{ background: '#111114' }} />
        ))}
      </div>
      {[1, 2, 3].map(i => (
        <div key={i} className="h-48 rounded-2xl animate-pulse" style={{ background: '#111114' }} />
      ))}
    </div>
  );
}

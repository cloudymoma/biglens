import React, { useState, useEffect, useMemo } from 'react';
import {
  ChevronDown, Server, Layers, Search, RefreshCw,
  Database, BarChart3, DollarSign, Lightbulb, Clock,
} from 'lucide-react';
import type { QueryFilters } from './types';
import { fetchConfig, fetchDatasets, fetchTables, fetchRegions } from './api';
import StorageDashboard from './dashboards/StorageDashboard';
import ComputeDashboard from './dashboards/ComputeDashboard';
import CostDashboard from './dashboards/CostDashboard';
import InsightsDashboard from './dashboards/InsightsDashboard';

type Tab = 'storage' | 'compute' | 'cost' | 'insights';

const TABS: { id: Tab; label: string; icon: React.ReactNode }[] = [
  { id: 'storage',  label: 'Storage',  icon: <Database size={16} /> },
  { id: 'compute',  label: 'Compute',  icon: <BarChart3 size={16} /> },
  { id: 'cost',     label: 'Cost',     icon: <DollarSign size={16} /> },
  { id: 'insights', label: 'Insights', icon: <Lightbulb size={16} /> },
];

function App() {
  const [activeTab, setActiveTab] = useState<Tab>('storage');
  const [config, setConfig] = useState<any>(null);
  const [regions, setRegions] = useState<string[]>([]);
  const [datasets, setDatasets] = useState<string[]>([]);
  const [tables, setTables] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);

  const [selectedRegion, setSelectedRegion] = useState('us');
  const [selectedDataset, setSelectedDataset] = useState('');
  const [selectedTable, setSelectedTable] = useState('');
  const [userEmail, setUserEmail] = useState('');
  const [timeRange, setTimeRange] = useState('7d');

  const [refreshKey, setRefreshKey] = useState(0);

  useEffect(() => {
    Promise.all([fetchConfig(), fetchDatasets(), fetchRegions()])
      .then(([cfg, ds, rg]) => { setConfig(cfg); setDatasets(ds); setRegions(rg); })
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    if (selectedDataset) {
      fetchTables(selectedDataset).then(setTables);
    } else {
      setTables([]);
      setSelectedTable('');
    }
  }, [selectedDataset]);

  const filters: QueryFilters = useMemo(() => ({
    region: selectedRegion,
    dataset: selectedDataset,
    table: selectedTable,
    user_email: userEmail,
    time_range: timeRange,
  }), [selectedRegion, selectedDataset, selectedTable, userEmail, timeRange, refreshKey]);

  if (loading) {
    return (
      <div className="flex h-screen w-full items-center justify-center" style={{ background: '#09090b' }}>
        <div className="flex flex-col items-center gap-8">
          <div className="relative">
            <div className="absolute inset-0 rounded-full blur-2xl opacity-30 animate-pulse" style={{ background: 'radial-gradient(circle, #38bdf8, transparent)' }} />
            <div className="relative z-10 h-14 w-14 rounded-full border-2 border-transparent animate-spin" style={{ borderTopColor: '#38bdf8', borderRightColor: '#38bdf820' }} />
          </div>
          <div className="text-center">
            <p className="text-zinc-400 text-sm font-medium tracking-wide">Initializing BigLens</p>
            <p className="text-zinc-600 text-xs mt-1 font-mono">Connecting to BigQuery...</p>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="flex h-screen w-full text-zinc-100 overflow-hidden" style={{ background: '#09090b' }}>

      {/* Sidebar */}
      <aside className="w-[260px] border-r border-zinc-800/60 flex flex-col shrink-0" style={{ background: '#0c0c0f' }}>
        {/* Logo */}
        <div className="px-5 pt-6 pb-4">
          <div className="flex items-center gap-3">
            <div className="relative">
              <div className="absolute inset-0 rounded-xl blur-md opacity-40" style={{ background: '#38bdf8' }} />
              <div className="relative p-2 rounded-xl border border-cyan-500/20" style={{ background: 'linear-gradient(135deg, #0e7490, #164e63)' }}>
                <Layers className="text-cyan-300" size={20} strokeWidth={2.5} />
              </div>
            </div>
            <div>
              <h1 className="text-lg font-bold tracking-tight text-white leading-none">BigLens</h1>
              <span className="text-[10px] font-mono text-cyan-400/70 uppercase tracking-[0.2em]">Analytics</span>
            </div>
          </div>
        </div>

        <div className="mx-4 border-t border-zinc-800/40" />

        {/* Dashboard tabs */}
        <nav className="flex flex-col gap-0.5 px-3 mt-4">
          {TABS.map(tab => (
            <button
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={`flex items-center gap-2.5 px-3 py-2 rounded-lg cursor-pointer transition-all text-sm text-left w-full ${
                activeTab === tab.id
                  ? 'text-white font-medium'
                  : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/30'
              }`}
              style={activeTab === tab.id ? { background: '#18181b', boxShadow: 'inset 0 0 0 1px rgba(63,63,70,0.4)' } : undefined}
            >
              <span className={activeTab === tab.id ? 'text-cyan-400' : ''}>{tab.icon}</span>
              <span>{tab.label}</span>
            </button>
          ))}
        </nav>

        {/* Global Filters */}
        <div className="px-3 mt-6">
          <div className="flex items-center gap-2 px-3 mb-3">
            <Search size={12} className="text-zinc-600" />
            <p className="text-[10px] font-semibold text-zinc-600 uppercase tracking-[0.15em]">Filters</p>
          </div>
          <div className="flex flex-col gap-3 p-3 rounded-xl border border-zinc-800/40" style={{ background: '#111114' }}>
            <FilterCombobox label="Region" value={selectedRegion} options={regions}
              onChange={setSelectedRegion} />
            <FilterSelect label="Dataset" value={selectedDataset} options={datasets}
              onChange={v => { setSelectedDataset(v); setSelectedTable(''); }} />
            <FilterSelect label="Table" value={selectedTable} options={tables}
              onChange={setSelectedTable} disabled={!selectedDataset} />
            <FilterInput label="User Email" value={userEmail} onChange={setUserEmail}
              placeholder="filter@example.com" />
            <FilterSelect label="Time Range" value={timeRange}
              options={['1d', '7d', '30d', '90d']}
              optionLabels={['Last 24h', 'Last 7 days', 'Last 30 days', 'Last 90 days']}
              onChange={setTimeRange} />
          </div>
        </div>

        {/* Project info */}
        <div className="mt-auto px-3 pb-4">
          <div className="p-3 rounded-xl border border-zinc-800/40 flex items-center gap-3" style={{ background: '#111114' }}>
            <div className="p-2 rounded-lg border border-emerald-500/15" style={{ background: '#052e1610' }}>
              <div className="relative">
                <span className="absolute -top-0.5 -right-0.5 h-2 w-2 rounded-full bg-emerald-400 animate-pulse" />
                <Server size={15} className="text-emerald-400" />
              </div>
            </div>
            <div className="flex-1 min-w-0">
              <p className="text-[9px] text-zinc-600 uppercase font-semibold tracking-wider">Project</p>
              <p className="text-xs font-mono truncate text-zinc-400">{config?.ProjectID || 'Not connected'}</p>
            </div>
          </div>
        </div>
      </aside>

      {/* Main Content */}
      <main className="flex-1 overflow-y-auto relative" style={{ background: '#09090b' }}>
        <div className="absolute top-0 left-1/2 -translate-x-1/2 w-[800px] h-[600px] pointer-events-none" style={{ background: 'radial-gradient(ellipse at center top, rgba(56,189,248,0.06) 0%, transparent 60%)' }} />

        <div className="p-8 max-w-[1400px] mx-auto relative z-10">
          {/* Header */}
          <header className="flex justify-between items-end mb-8">
            <div>
              <h2 className="text-2xl font-bold tracking-tight text-white">
                {TABS.find(t => t.id === activeTab)?.label} Analysis
              </h2>
              <p className="text-xs text-zinc-500 mt-1 flex items-center gap-1.5">
                <Clock size={11} />
                <span className="text-zinc-600">region-{selectedRegion}</span>
                <span>|</span>
                {timeRange === '1d' ? 'Last 24 hours' : timeRange === '7d' ? 'Last 7 days' : timeRange === '30d' ? 'Last 30 days' : 'Last 90 days'}
                {selectedDataset && <span className="text-zinc-600">| {selectedDataset}{selectedTable ? `.${selectedTable}` : ''}</span>}
              </p>
            </div>

            <button
              onClick={() => setRefreshKey(k => k + 1)}
              className="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium transition-all border cursor-pointer"
              style={{ background: 'linear-gradient(135deg, #0e7490, #164e63)', borderColor: '#0e749060', color: '#e0f2fe' }}
            >
              <RefreshCw size={14} />
              Refresh
            </button>
          </header>

          {/* Dashboard content */}
          {activeTab === 'storage' && <StorageDashboard filters={filters} />}
          {activeTab === 'compute' && <ComputeDashboard filters={filters} />}
          {activeTab === 'cost' && <CostDashboard filters={filters} />}
          {activeTab === 'insights' && <InsightsDashboard filters={filters} />}
        </div>
      </main>
    </div>
  );
}

function FilterSelect({ label, value, options, optionLabels, onChange, disabled }: {
  label: string; value: string; options: string[]; optionLabels?: string[];
  onChange: (v: string) => void; disabled?: boolean;
}) {
  return (
    <div className={disabled ? 'opacity-30 pointer-events-none' : ''}>
      <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">{label}</label>
      <div className="relative">
        <select
          value={value}
          onChange={e => onChange(e.target.value)}
          disabled={disabled}
          className="w-full text-xs text-zinc-400 rounded-lg pl-3 pr-8 py-2 outline-none cursor-pointer appearance-none border border-zinc-800/50 transition-colors focus:border-cyan-500/30"
          style={{ background: '#09090b' }}
        >
          {!optionLabels && <option value="">All {label}s</option>}
          {options.map((opt, i) => (
            <option key={opt} value={opt}>{optionLabels ? optionLabels[i] : opt}</option>
          ))}
        </select>
        <ChevronDown size={12} className="absolute right-2.5 top-1/2 -translate-y-1/2 pointer-events-none text-zinc-600" />
      </div>
    </div>
  );
}

function FilterCombobox({ label, value, options, onChange }: {
  label: string; value: string; options: string[]; onChange: (v: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const ref = React.useRef<HTMLDivElement>(null);

  const filtered = query
    ? options.filter(o => o.toLowerCase().includes(query.toLowerCase()))
    : options;

  React.useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, []);

  return (
    <div ref={ref} className="relative">
      <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">{label}</label>
      <button
        onClick={() => { setOpen(!open); setQuery(''); }}
        className="w-full text-xs text-zinc-400 rounded-lg pl-3 pr-8 py-2 text-left border border-zinc-800/50 transition-colors hover:border-zinc-700/60 cursor-pointer"
        style={{ background: '#09090b' }}
      >
        {value}
        <ChevronDown size={12} className={`absolute right-2.5 top-[calc(50%+8px)] -translate-y-1/2 text-zinc-600 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>

      {open && (
        <div className="absolute z-50 mt-1 w-full rounded-lg border border-zinc-800/60 shadow-xl overflow-hidden" style={{ background: '#111114' }}>
          <div className="p-1.5">
            <input
              autoFocus
              value={query}
              onChange={e => setQuery(e.target.value)}
              placeholder="Type to filter..."
              className="w-full text-xs text-zinc-300 rounded-md px-2.5 py-1.5 outline-none border border-zinc-800/50 focus:border-cyan-500/30 placeholder:text-zinc-700"
              style={{ background: '#09090b' }}
            />
          </div>
          <div className="max-h-48 overflow-y-auto">
            {filtered.length > 0 ? filtered.map(opt => (
              <button
                key={opt}
                onClick={() => { onChange(opt); setOpen(false); setQuery(''); }}
                className={`w-full text-left px-3 py-1.5 text-xs cursor-pointer transition-colors ${
                  opt === value ? 'text-cyan-400 bg-cyan-500/5' : 'text-zinc-400 hover:bg-zinc-800/50 hover:text-zinc-200'
                }`}
              >
                {opt}
              </button>
            )) : (
              <p className="px-3 py-2 text-[11px] text-zinc-600">No matches</p>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function FilterInput({ label, value, onChange, placeholder }: {
  label: string; value: string; onChange: (v: string) => void; placeholder?: string;
}) {
  return (
    <div>
      <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">{label}</label>
      <input
        type="text"
        value={value}
        onChange={e => onChange(e.target.value)}
        placeholder={placeholder}
        className="w-full text-xs text-zinc-400 rounded-lg px-3 py-2 outline-none border border-zinc-800/50 transition-colors focus:border-cyan-500/30 placeholder:text-zinc-700"
        style={{ background: '#09090b' }}
      />
    </div>
  );
}

export default App;

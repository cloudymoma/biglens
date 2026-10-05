import { useEffect, useState } from 'react';
import type React from 'react';
import { Settings } from 'lucide-react';
import { fetchBillingConfig, fetchBillingMeta } from '../api';
import type { BillingDatasetInfo, BillingFilterState, BillingMeta } from '../types';
import { EmptyState, ErrorBanner } from '../dashboards/shared';
import ConfigPanel from './ConfigPanel';
import { Panel } from './shared';
import FilterBar from './FilterBar';
import OverviewTab from './OverviewTab';
import ServicesTab from './ServicesTab';
import ProjectsTab from './ProjectsTab';
import ResourcesTab from './ResourcesTab';
import CreditsTab from './CreditsTab';
import PricingTab from './PricingTab';

const TABS = [
  { id: 'overview', label: 'Overview' },
  { id: 'services', label: 'Services' },
  { id: 'projects', label: 'Projects' },
  { id: 'resources', label: 'Resources' },
  { id: 'credits', label: 'Credits' },
  { id: 'pricing', label: 'Pricing' },
] as const;

type TabId = (typeof TABS)[number]['id'];

function pacificDateOffset(days: number): string {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: 'America/Los_Angeles',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(new Date());
  const y = Number(parts.find(p => p.type === 'year')?.value ?? 1970);
  const m = Number(parts.find(p => p.type === 'month')?.value ?? 1);
  const d = Number(parts.find(p => p.type === 'day')?.value ?? 1);
  const dt = new Date(Date.UTC(y, m - 1, d + days));
  return dt.toISOString().slice(0, 10);
}

function defaultBillingFilter(dataset: string): BillingFilterState {
  return {
    dataset,
    start: pacificDateOffset(-30),
    end: pacificDateOffset(0),
    invoiceMonth: '',
    accounts: [],
    projects: [],
    services: [],
    labelKey: '',
    labelValue: '',
  };
}

export default function BillingDashboard() {
  const [datasets, setDatasets] = useState<BillingDatasetInfo[] | null>(null);
  const [configError, setConfigError] = useState('');
  const [showConfig, setShowConfig] = useState(false);
  const [filter, setFilter] = useState<BillingFilterState | null>(null);
  const [meta, setMeta] = useState<BillingMeta | null>(null);
  const [active, setActive] = useState<TabId>('overview');

  useEffect(() => {
    let cancelled = false;
    fetchBillingConfig()
      .then(resp => {
        if (cancelled) return;
        setDatasets(resp.datasets);
        const first = resp.datasets.find(d => !d.error);
        if (first) setFilter(defaultBillingFilter(first.dataset));
      })
      .catch(e => {
        if (!cancelled) setConfigError(e.response?.data || e.message);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const dataset = filter?.dataset ?? '';
  useEffect(() => {
    if (!dataset) return;
    let cancelled = false;
    setMeta(null);
    fetchBillingMeta(dataset)
      .then(m => {
        if (!cancelled) setMeta(m);
      })
      .catch(e => {
        if (!cancelled) setConfigError(e.response?.data || e.message);
      });
    return () => {
      cancelled = true;
    };
  }, [dataset]);

  if (configError) return <ErrorBanner message={configError} />;
  if (datasets === null) return <EmptyState text="Loading billing configuration…" />;

  const usable = datasets.filter(d => !d.error);
  if (usable.length === 0 || showConfig) {
    return (
      <Panel title="GCP Billing datasets" note="stored in conf.yaml">
        <ConfigPanel
          datasets={datasets}
          onChanged={ds => {
            setDatasets(ds);
            const first = ds.find(d => !d.error);
            if (first && (!filter || !ds.some(d => d.dataset === filter.dataset && !d.error))) {
              setFilter(defaultBillingFilter(first.dataset));
            }
            if (ds.some(d => !d.error)) setShowConfig(false);
          }}
        />
      </Panel>
    );
  }
  if (!filter || !meta) return <EmptyState text="Loading billing metadata…" />;

  const tabBody: Record<TabId, React.ReactNode> = {
    overview: <OverviewTab filter={filter} meta={meta} />,
    services: <ServicesTab filter={filter} meta={meta} />,
    projects: <ProjectsTab filter={filter} meta={meta} />,
    resources: <ResourcesTab filter={filter} meta={meta} />,
    credits: <CreditsTab filter={filter} meta={meta} />,
    pricing: <PricingTab filter={filter} meta={meta} />,
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 flex-wrap">
        <select
          value={filter.dataset}
          onChange={e => setFilter(defaultBillingFilter(e.target.value))}
          className="bg-zinc-900 border border-zinc-700 rounded-lg px-2 py-1.5 text-xs text-zinc-200"
        >
          {usable.map(d => (
            <option key={d.dataset} value={d.dataset}>{d.dataset}</option>
          ))}
        </select>
        <button
          onClick={() => setShowConfig(true)}
          className="p-1.5 rounded-lg text-zinc-500 hover:text-zinc-300"
          title="Manage datasets"
        >
          <Settings size={14} />
        </button>
        <FilterBar filter={filter} meta={meta} onChange={setFilter} />
      </div>
      <div className="flex items-center gap-1 border-b border-zinc-800/60 pb-2">
        {TABS.map(t => (
          <button
            key={t.id}
            onClick={() => setActive(t.id)}
            className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors ${
              active === t.id ? 'bg-zinc-800 text-white' : 'text-zinc-500 hover:text-zinc-300'
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>
      <div>{tabBody[active]}</div>
    </div>
  );
}

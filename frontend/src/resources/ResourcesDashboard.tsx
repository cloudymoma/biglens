import { useEffect, useState } from 'react';
import { RefreshCw, Settings } from 'lucide-react';
import { fetchResourcesConfig } from '../api';
import type { ResProjectInfo } from '../types';
import { EmptyState, ErrorBanner } from '../dashboards/shared';
import { Panel } from './shared';
import ConfigPanel from './ConfigPanel';
import OverviewTab from './OverviewTab';
import ComputeTab from './ComputeTab';
import StorageTab from './StorageTab';
import NetworkTab from './NetworkTab';
import ExplorerTab from './ExplorerTab';
import InsightsTab from './InsightsTab';

const TABS = [
  { id: 'overview', label: 'Overview' },
  { id: 'compute', label: 'Compute' },
  { id: 'storage', label: 'Storage' },
  { id: 'network', label: 'Network' },
  { id: 'explorer', label: 'Explorer' },
  { id: 'insights', label: 'Insights' },
] as const;

type TabId = (typeof TABS)[number]['id'];

export default function ResourcesDashboard() {
  const [projects, setProjects] = useState<ResProjectInfo[] | null>(null);
  const [configError, setConfigError] = useState('');
  const [showConfig, setShowConfig] = useState(false);
  const [project, setProject] = useState('');
  const [active, setActive] = useState<TabId>('overview');
  const [visited, setVisited] = useState<ReadonlySet<TabId>>(new Set<TabId>(['overview']));
  const [refreshKey, setRefreshKey] = useState(0);

  useEffect(() => {
    fetchResourcesConfig()
      .then(resp => {
        setProjects(resp.projects);
        const first = resp.projects.find(p => !p.error);
        if (first) setProject(first.project);
      })
      .catch(e => setConfigError(e.response?.data || e.message));
  }, []);

  if (configError) return <ErrorBanner message={configError} />;
  if (projects === null) return <EmptyState text="Loading GCP Resources configuration…" />;

  const usable = projects.filter(p => !p.error);
  if (usable.length === 0 || showConfig) {
    return (
      <Panel title="GCP Resources projects" note="stored in conf.yaml">
        {showConfig && usable.length > 0 && (
          <button
            onClick={() => setShowConfig(false)}
            className="mb-2 text-xs text-zinc-400 hover:text-zinc-200"
          >
            ← Back to dashboard
          </button>
        )}
        <ConfigPanel
          projects={projects}
          onChanged={resp => {
            setProjects(resp.projects);
            const first = resp.projects.find(p => !p.error);
            if (first && (!project || !resp.projects.some(p => p.project === project && !p.error))) {
              setProject(first.project);
            }
            if (resp.projects.some(p => !p.error)) setShowConfig(false);
          }}
        />
      </Panel>
    );
  }
  if (!project) return <EmptyState text="Select a project…" />;

  const select = (id: TabId) => {
    setActive(id);
    setVisited(prev => (prev.has(id) ? prev : new Set(prev).add(id)));
  };

  const switchProject = (p: string) => {
    setProject(p);
    // Remount all tabs so each re-fetches for the new project.
    setVisited(new Set<TabId>([active]));
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <select
          value={project}
          onChange={e => switchProject(e.target.value)}
          className="bg-zinc-900 border border-zinc-700 rounded-lg px-2 py-1.5 text-xs text-zinc-200 font-mono"
        >
          {usable.map(p => (
            <option key={p.project} value={p.project}>{p.project}</option>
          ))}
        </select>
        <button
          onClick={() => setShowConfig(true)}
          title="Manage projects"
          className="p-1.5 rounded-lg border border-zinc-800 text-zinc-400 hover:text-zinc-200"
        >
          <Settings size={14} />
        </button>
        <button
          onClick={() => setRefreshKey(k => k + 1)}
          title="Refresh (bypass server cache)"
          className="p-1.5 rounded-lg border border-zinc-800 text-zinc-400 hover:text-zinc-200"
        >
          <RefreshCw size={14} />
        </button>
        <div className="flex-1" />
        <nav className="flex gap-1">
          {TABS.map(t => (
            <button
              key={t.id}
              onClick={() => select(t.id)}
              className={`px-3 py-1.5 rounded-lg text-xs ${
                active === t.id ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-400 hover:text-zinc-200'
              }`}
            >
              {t.label}
            </button>
          ))}
        </nav>
      </div>

      {TABS.map(t =>
        visited.has(t.id) ? (
          <div key={`${t.id}:${project}`} className={active === t.id ? '' : 'hidden'}>
            {t.id === 'overview' && <OverviewTab project={project} refreshKey={refreshKey} />}
            {t.id === 'compute' && <ComputeTab project={project} refreshKey={refreshKey} />}
            {t.id === 'storage' && <StorageTab project={project} refreshKey={refreshKey} />}
            {t.id === 'network' && <NetworkTab project={project} refreshKey={refreshKey} />}
            {t.id === 'explorer' && <ExplorerTab project={project} refreshKey={refreshKey} />}
            {t.id === 'insights' && <InsightsTab project={project} refreshKey={refreshKey} />}
          </div>
        ) : null,
      )}
    </div>
  );
}

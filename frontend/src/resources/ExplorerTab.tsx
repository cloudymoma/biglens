import { useEffect, useState } from 'react';
import { Search } from 'lucide-react';
import { fetchResourcesExplorer } from '../api';
import type { ResExplorerData } from '../types';
import { EmptyState, ErrorBanner } from '../dashboards/shared';
import { Panel, th, td, type ResTabProps } from './shared';

export default function ExplorerTab({ project, refreshKey }: ResTabProps) {
  const [query, setQuery] = useState('');
  const [assetType, setAssetType] = useState('');
  const [applied, setApplied] = useState({ query: '', assetType: '' });
  const [data, setData] = useState<ResExplorerData | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setData(null);
    setError('');
    fetchResourcesExplorer(project, applied.query, applied.assetType, refreshKey > 0)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message));
  }, [project, refreshKey, applied]);

  const types = data ? [...new Set(data.items.map(i => i.asset_type))].sort() : [];

  return (
    <Panel title="All resources" note={data ? `fetched ${data.fetched_at}` : undefined}>
      <div className="flex gap-2 mb-3">
        <div className="relative flex-1">
          <Search size={14} className="absolute left-2.5 top-2 text-zinc-500" />
          <input
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') setApplied({ query, assetType }); }}
            placeholder="Search resources (free text, e.g. name, label value)…"
            className="w-full bg-zinc-900 border border-zinc-700 rounded-lg pl-8 pr-3 py-1.5 text-xs text-zinc-200"
          />
        </div>
        <select
          value={assetType}
          onChange={e => { setAssetType(e.target.value); setApplied({ query, assetType: e.target.value }); }}
          className="bg-zinc-900 border border-zinc-700 rounded-lg px-2 py-1.5 text-xs text-zinc-200"
        >
          <option value="">All types</option>
          {types.map(t => <option key={t} value={t}>{t}</option>)}
        </select>
        <button
          onClick={() => setApplied({ query, assetType })}
          className="px-3 py-1.5 rounded-lg text-xs bg-sky-600/80 hover:bg-sky-600 text-white"
        >
          Search
        </button>
      </div>
      {error ? <ErrorBanner message={error} />
        : !data ? <EmptyState text="Searching inventory…" />
        : data.items.length === 0 ? <EmptyState text="No resources match." />
        : (
          <>
            {data.truncated && (
              <p className="text-[11px] text-amber-300 mb-2">Results truncated at 1000 — refine the search.</p>
            )}
            <table className="w-full text-sm">
              <thead><tr><th className={th}>Name</th><th className={th}>Type</th><th className={th}>Location</th><th className={th}>State</th><th className={th}>Created</th></tr></thead>
              <tbody>
                {data.items.map(a => (
                  <tr key={a.name} className="border-t border-zinc-800/40">
                    <td className={td}>{a.display_name || a.name.split('/').pop()}</td>
                    <td className={td}>{a.asset_type}</td>
                    <td className={td}>{a.location}</td>
                    <td className={td}>{a.state}</td>
                    <td className={td}>{a.created ? a.created.slice(0, 10) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
        )}
    </Panel>
  );
}

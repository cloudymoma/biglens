import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Box, Square, RefreshCw, DownloadCloud, Network } from 'lucide-react';
import type { CatalogGraph, ConceptDetail, GraphNode, CatalogTypeCount, Concept, CatalogManifest, ImportResult } from '../types';
import {
  fetchCatalogGraph, fetchCatalogTypes, searchCatalog,
  fetchConcept, saveConcept, deleteConcept, importCatalog,
  fetchCatalogManifest, refreshCatalogImport,
} from '../api';
import { colorForEdge, colorForType } from './colors';
import BottomTabs, { type CatalogTab } from './BottomTabs';
import GraphErrorBoundary from './GraphErrorBoundary';
import { ErrorBanner } from '../dashboards/shared';

// Defer the heavy three.js / force-graph bundle until the view actually renders.
const GraphCanvas = lazy(() => import('./GraphCanvas'));

export default function CatalogView() {
  const [graph, setGraph] = useState<CatalogGraph>({ nodes: [], edges: [] });
  const [types, setTypes] = useState<CatalogTypeCount[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const webglOK = useMemo(webglAvailable, []);
  // Edge kinds present in the graph, in a stable display order, for the legend.
  const edgeKinds = useMemo(() => {
    const present = new Set(graph.edges.map(e => e.kind || 'reference'));
    return ['containment', 'lineage', 'definition', 'reference'].filter(k => present.has(k));
  }, [graph.edges]);
  const [mode, setMode] = useState<'2d' | '3d'>('2d');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [detail, setDetail] = useState<ConceptDetail | null>(null);

  const [tab, setTab] = useState<CatalogTab>('search');
  const [query, setQuery] = useState('');
  const [typeFilter, setTypeFilter] = useState('');
  const [tagFilter, setTagFilter] = useState('');
  const [results, setResults] = useState<GraphNode[]>([]);

  const [importQ, setImportQ] = useState('');
  const [importing, setImporting] = useState(false);
  const [manifest, setManifest] = useState<CatalogManifest | null>(null);
  const [notice, setNotice] = useState('');
  const [saving, setSaving] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);

  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined);

  // Load graph + types.
  useEffect(() => {
    setLoading(true);
    setError('');
    Promise.all([fetchCatalogGraph(), fetchCatalogTypes()])
      .then(([g, t]) => { setGraph(g); setTypes(t); })
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
    fetchCatalogManifest().then(setManifest); // null when nothing imported yet
  }, [refreshKey]);

  // Debounced search.
  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      searchCatalog(query, typeFilter, tagFilter).then(setResults).catch(() => setResults([]));
    }, 200);
  }, [query, typeFilter, tagFilter, refreshKey]);

  // Distinct tags across the graph, for the search facet dropdown.
  const allTags = useMemo(
    () => [...new Set(graph.nodes.flatMap(n => n.tags ?? []))].sort(),
    [graph.nodes],
  );

  const select = useCallback((id: string) => {
    setSelectedId(id);
    setTab('details');
    fetchConcept(id).then(setDetail).catch(() => setDetail(null));
  }, []);

  const handleSave = async (c: Concept) => {
    setSaving(true);
    setError('');
    try {
      const d = await saveConcept(c);
      setDetail(d);
      setSelectedId(d.concept.id);
      setRefreshKey(k => k + 1);
    } catch (e: any) {
      setError(e.response?.data || e.message);
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (id: string) => {
    setSaving(true);
    try {
      await deleteConcept(id);
      setDetail(null);
      setSelectedId(null);
      setRefreshKey(k => k + 1);
    } catch (e: any) {
      setError(e.response?.data || e.message);
    } finally {
      setSaving(false);
    }
  };

  const handleNew = () => { setDetail(null); setSelectedId(null); setTab('edit'); };

  const runImport = async (run: () => Promise<ImportResult>) => {
    setImporting(true);
    setError('');
    setNotice('');
    try {
      const res = await run();
      let msg = `Imported ${res.imported} concepts · ${res.containment_edges} containment + ${res.lineage_edges} lineage + ${res.definition_edges} definition edges`;
      if (res.duplicate_entries) msg += ` · ${res.duplicate_entries} duplicate search results skipped`;
      if (res.id_collisions) msg += ` · ${res.id_collisions} path collisions disambiguated`;
      if (res.preserved > 0) msg += ` · ${res.preserved} preserved (user-managed)`;
      if (res.pruned > 0) msg += ` · ${res.pruned} pruned`;
      if (res.lineage_dropped > 0) msg += ` · ${res.lineage_dropped} lineage edges dropped (out of scope)`;
      if (res.definition_dropped > 0) msg += ` · ${res.definition_dropped} definition links dropped (term or asset not imported)`;
      if (res.definition_error) msg += ` · entry links skipped: ${res.definition_error}`;
      if (res.truncated) msg += ` · result truncated at ${res.imported}`;
      if (res.lineage_error) msg += ` · lineage skipped: ${res.lineage_error}`;
      if (res.prune_error) msg += ` · prune failed: ${res.prune_error}`;
      if (res.aspect_error) msg += ` · aspects skipped: ${res.aspect_error}`;
      if (res.elapsed_ms) msg += ` (${res.elapsed_ms}ms)`;
      setNotice(msg);
      setRefreshKey(k => k + 1);
    } catch (e: any) {
      setError(e.response?.data || e.message);
    } finally {
      setImporting(false);
    }
  };

  const handleImport = () => runImport(() => importCatalog(importQ));
  // Re-run the import recorded in the bundle manifest (same query/scope).
  const handleRefreshImport = () => runImport(refreshCatalogImport);

  return (
    <div className="space-y-4">
      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-3">
        <div className="flex items-center gap-2 text-xs text-zinc-500">
          <Network size={14} className="text-cyan-400" />
          <span className="font-mono">{graph.nodes.length} nodes · {graph.edges.length} edges</span>
        </div>

        <div className="flex-1" />

        <div className="flex items-center gap-2 px-2 py-1.5 rounded-lg border border-zinc-800/50" style={{ background: '#111114' }}>
          <input
            value={importQ}
            onChange={e => setImportQ(e.target.value)}
            placeholder="Import query (e.g. type=TABLE)"
            className="text-xs text-zinc-300 bg-transparent outline-none w-48 placeholder:text-zinc-700"
          />
          <button
            onClick={handleImport}
            disabled={importing}
            className="flex items-center gap-1.5 text-xs text-cyan-400 hover:text-cyan-300 cursor-pointer disabled:opacity-40"
            title="Import entries from Dataplex into the OKF bundle"
          >
            <DownloadCloud size={13} /> {importing ? 'Importing...' : 'Import'}
          </button>
        </div>

        <div className="flex items-center rounded-lg border border-zinc-800/50 overflow-hidden" style={{ background: '#09090b' }}>
          <ModeButton active={mode === '2d'} onClick={() => setMode('2d')} icon={<Square size={13} />} label="2D" />
          <ModeButton
            active={mode === '3d'}
            onClick={() => webglOK && setMode('3d')}
            icon={<Box size={13} />}
            label="3D"
            disabled={!webglOK}
            title={webglOK ? '' : 'WebGL unavailable in this browser — enable hardware acceleration to use 3D'}
          />
        </div>

        <button
          onClick={() => setRefreshKey(k => k + 1)}
          className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs font-medium border border-zinc-800/50 text-zinc-400 hover:text-zinc-200 cursor-pointer transition-all"
          style={{ background: '#111114' }}
        >
          <RefreshCw size={13} /> Refresh
        </button>
      </div>

      {/* Last-import manifest line */}
      {manifest && (
        <div className="flex flex-wrap items-center gap-2 text-[11px] text-zinc-600">
          <span>
            Last imported {new Date(manifest.imported_at).toLocaleString()} · query “{manifest.query}” ·{' '}
            {manifest.project} ({manifest.location}){manifest.truncated ? ' · truncated' : ''}
          </span>
          <button
            onClick={handleRefreshImport}
            disabled={importing}
            className="flex items-center gap-1 text-cyan-500 hover:text-cyan-300 cursor-pointer disabled:opacity-40"
            title="Re-run the recorded import with the same query and scope"
          >
            <RefreshCw size={11} /> {importing ? 'Re-importing…' : 'Re-import'}
          </button>
        </div>
      )}

      {error && <ErrorBanner message={error} />}
      {notice && (
        <div
          className={`flex items-center gap-2.5 px-4 py-2.5 rounded-xl border text-xs ${
            notice.includes('lineage skipped')
              ? 'border-amber-500/20 bg-amber-500/5 text-amber-400'
              : 'border-cyan-500/20 bg-cyan-500/5 text-cyan-300'
          }`}
        >
          {notice}
        </div>
      )}

      {/* Graph canvas */}
      <div className="rounded-2xl border border-zinc-800/50 overflow-hidden relative" style={{ background: '#09090b', height: '56vh' }}>
        {loading ? (
          <div className="absolute inset-0 flex items-center justify-center text-zinc-600 text-xs">Loading catalog…</div>
        ) : graph.nodes.length === 0 ? (
          <EmptyGraph importing={importing} onImport={handleImport} />
        ) : (
          <Suspense fallback={<div className="absolute inset-0 flex items-center justify-center text-zinc-600 text-xs">Rendering graph…</div>}>
            <GraphErrorBoundary key={mode}>
              <GraphCanvas graph={graph} mode={mode} selectedId={selectedId} onSelect={select} />
            </GraphErrorBoundary>
          </Suspense>
        )}

        {/* Legend */}
        {types.length > 0 && (
          <div className="absolute top-3 left-3 p-2.5 rounded-xl border border-zinc-800/60 max-w-[220px]" style={{ background: 'rgba(17,17,20,0.85)' }}>
            <p className="text-[9px] font-semibold text-zinc-600 uppercase tracking-wider mb-1.5">Types</p>
            <div className="flex flex-col gap-1">
              {types.map(t => (
                <div key={t.type} className="flex items-center gap-1.5 text-[10px] text-zinc-400">
                  <span className="w-2 h-2 rounded-full" style={{ background: colorForType(t.type) }} />
                  <span className="truncate">{t.type}</span>
                  <span className="ml-auto text-zinc-600 font-mono">{t.count}</span>
                </div>
              ))}
            </div>
            {edgeKinds.length > 0 && (
              <>
                <p className="text-[9px] font-semibold text-zinc-600 uppercase tracking-wider mt-2 mb-1.5">Edges</p>
                <div className="flex flex-col gap-1">
                  {edgeKinds.map(k => (
                    <div key={k} className="flex items-center gap-1.5 text-[10px] text-zinc-400">
                      <span
                        className="w-3"
                        style={{ borderTop: `2px ${k === 'definition' ? 'dashed' : 'solid'} ${colorForEdge(k)}` }}
                      />
                      <span className="capitalize">{k}</span>
                    </div>
                  ))}
                </div>
              </>
            )}
          </div>
        )}
      </div>

      {/* Bottom tabs: Search / Details / Edit */}
      <BottomTabs
        active={tab}
        onTabChange={setTab}
        query={query}
        onQuery={setQuery}
        typeFilter={typeFilter}
        onTypeFilter={setTypeFilter}
        tagFilter={tagFilter}
        onTagFilter={setTagFilter}
        tags={allTags}
        types={types}
        results={results}
        onSelect={select}
        detail={detail}
        onSave={handleSave}
        onDelete={handleDelete}
        saving={saving}
        onNew={handleNew}
      />
    </div>
  );
}

function ModeButton({ active, onClick, icon, label, disabled, title }: {
  active: boolean; onClick: () => void; icon: React.ReactNode; label: string;
  disabled?: boolean; title?: string;
}) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      title={title}
      className={`flex items-center gap-1.5 px-3 py-2 text-xs font-medium transition-all ${
        disabled
          ? 'text-zinc-700 cursor-not-allowed'
          : active ? 'text-cyan-400 cursor-pointer' : 'text-zinc-600 hover:text-zinc-400 cursor-pointer'
      }`}
      style={active && !disabled ? { background: '#18181b' } : undefined}
    >
      {icon}{label}
    </button>
  );
}

// webglAvailable reports whether the browser can create a WebGL context. The
// 3D graph requires it; on remote desktops / GPU-disabled browsers it fails,
// so we disable the 3D toggle rather than let it error.
function webglAvailable(): boolean {
  try {
    const canvas = document.createElement('canvas');
    return !!(
      window.WebGLRenderingContext &&
      (canvas.getContext('webgl') || canvas.getContext('experimental-webgl'))
    );
  } catch {
    return false;
  }
}

function EmptyGraph({ importing, onImport }: { importing: boolean; onImport: () => void }) {
  return (
    <div className="absolute inset-0 flex flex-col items-center justify-center gap-4 text-center px-6">
      <Network size={36} strokeWidth={1.5} className="text-zinc-700" />
      <div>
        <p className="text-sm font-medium text-zinc-400">No concepts in the bundle yet</p>
        <p className="text-xs text-zinc-600 mt-1 max-w-sm">
          Import metadata from your Dataplex Knowledge Catalog, or add concepts manually in the Edit tab.
        </p>
      </div>
      <button
        onClick={onImport}
        disabled={importing}
        className="flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-medium cursor-pointer disabled:opacity-40"
        style={{ background: 'linear-gradient(135deg, #0e7490, #164e63)', color: '#e0f2fe' }}
      >
        <DownloadCloud size={14} /> {importing ? 'Importing…' : 'Import from Dataplex'}
      </button>
    </div>
  );
}

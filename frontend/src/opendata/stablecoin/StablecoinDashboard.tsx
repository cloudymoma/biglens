import { useEffect, useState } from 'react';
import { CircleDollarSign, Moon, RefreshCw, Sun } from 'lucide-react';
import { usePolling } from '../common/usePolling';
import type { SourceStatus } from '../common/types';
import { fetchStableMeta, fetchStableOverview } from './api';
import { effectiveRange, parseView, TAB_RANGES, viewSearch } from './deeplink';
import type { StableAsset, StableRange, StableTab, StableView } from './deeplink';
import type { Theme } from './signals';
import { cx } from './styles';
import { initialTheme, StableThemeContext, storeTheme } from './theme';
import type { StableMeta } from './types';
import { AsOf, ColdState, ErrorNote, SegmentedControl, Sources, TabBar } from './ui';
import type { Choice } from './ui';

const ASSET_CHOICES: readonly Choice<StableAsset>[] = [
  { id: 'all', label: 'All' },
  { id: 'usdt', label: 'USDT' },
  { id: 'usdc', label: 'USDC' },
  { id: 'eurc', label: 'EURC' },
];

const TAB_CHOICES: readonly Choice<StableTab>[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'peg', label: 'Peg & Liquidity' },
  { id: 'supply', label: 'Supply & Flows' },
  { id: 'reserves', label: 'Reserves & Regulation' },
  { id: 'chains', label: 'Chains' },
  { id: 'controls', label: 'Controls & Freezes' },
];

const RANGE_LABELS: Record<StableRange, string> = { '24h': '24H', '7d': '7D', '30d': '30D', '90d': '90D', '1y': '1Y', all: 'ALL' };

const DISCLAIMER = 'Informational only, not financial advice. Signals are rule outputs, not ratings.';

// providerSources turns /meta provider health into source badges.
function providerSources(meta: StableMeta | null): SourceStatus[] {
  if (!meta) return [];
  return meta.providers
    .filter(p => p.status !== 'not_in_use')
    .map(p => ({
      id: p.key,
      label: p.label,
      status: p.enabled ? p.status : 'disabled',
      as_of: p.last_ok,
      error: p.status === 'error' || p.status === 'stale' ? p.last_error : undefined,
      license: p.attribution || undefined,
    }));
}

export default function StablecoinDashboard() {
  const [view, setView] = useState<StableView>(() => parseView(window.location.search));
  const [theme, setTheme] = useState<Theme>(initialTheme);
  const [mounted, setMounted] = useState<Record<StableTab, boolean>>(() => ({
    overview: false,
    peg: false,
    supply: false,
    reserves: false,
    chains: false,
    controls: false,
    [view.tab]: true,
  }));
  const [refreshTick, setRefreshTick] = useState(0);

  // Deep links (§7.3): the view lives in the query string, written with
  // replaceState whenever it changes (an external-system sync, no state set).
  useEffect(() => {
    const q = new URLSearchParams(window.location.search);
    q.set('dataset', 'stablecoin');
    const next = `${window.location.pathname}${viewSearch(view, q.toString())}${window.location.hash}`;
    if (next !== `${window.location.pathname}${window.location.search}${window.location.hash}`) {
      window.history.replaceState(null, '', next);
    }
  }, [view]);

  const metaPoll = usePolling(fetchStableMeta, 60_000, `stable-meta-${refreshTick}`);
  const overviewRange = effectiveRange('overview', view.range) ?? '90d';
  const overviewPoll = usePolling(
    () => fetchStableOverview(overviewRange),
    60_000,
    `stable-overview-${overviewRange}-${refreshTick}`,
  );

  const selectTab = (tab: StableTab) => {
    setView(v => ({ ...v, tab, range: effectiveRange(tab, v.range) }));
    setMounted(m => (m[tab] ? m : { ...m, [tab]: true }));
  };
  const toggleTheme = () => {
    const next: Theme = theme === 'dark' ? 'light' : 'dark';
    setTheme(next);
    storeTheme(next);
  };

  const tabRanges = TAB_RANGES[view.tab];
  const range = effectiveRange(view.tab, view.range);
  const updated = overviewPoll.data?.as_of;
  const sources = providerSources(metaPoll.data);

  return (
    <StableThemeContext.Provider value={theme}>
      <div className={theme === 'dark' ? 'dark' : ''}>
        <div className="space-y-5 rounded-2xl bg-zinc-50 p-5 text-zinc-900 dark:bg-transparent dark:p-0 dark:text-zinc-100">
          {/* Header */}
          <div className="flex flex-col gap-4 xl:flex-row xl:items-center xl:justify-between">
            <div className="flex items-center gap-2.5">
              <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-emerald-600/10 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-400">
                <CircleDollarSign className="h-5 w-5" aria-hidden="true" />
              </div>
              <div>
                <h1 className={`text-xl font-bold tracking-tight ${cx.strong}`}>Stablecoin Intelligence</h1>
                <p className={`text-xs ${cx.muted}`}>
                  USDT · USDC · EURC — peg, flows, supply, reserves, regulation and contract controls from public sources
                </p>
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-2.5">
              <SegmentedControl
                label="Asset"
                options={ASSET_CHOICES}
                value={view.asset}
                onChange={asset => setView(v => ({ ...v, asset }))}
              />
              {range !== null && tabRanges.length > 0 && (
                <SegmentedControl
                  label="Range"
                  options={tabRanges.map(r => ({ id: r, label: RANGE_LABELS[r] }))}
                  value={range}
                  onChange={r => setView(v => ({ ...v, range: r }))}
                />
              )}
              <button
                type="button"
                onClick={toggleTheme}
                aria-label={theme === 'dark' ? 'Switch to the light theme' : 'Switch to the dark theme'}
                className={`${cx.button} ${cx.focus}`}
              >
                {theme === 'dark' ? <Sun className="h-3.5 w-3.5" aria-hidden="true" /> : <Moon className="h-3.5 w-3.5" aria-hidden="true" />}
                <span>{theme === 'dark' ? 'Light' : 'Dark'}</span>
              </button>
              <button
                type="button"
                onClick={() => setRefreshTick(t => t + 1)}
                aria-label="Refresh the dashboard"
                className={`${cx.button} ${cx.focus}`}
              >
                <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
                <span>Refresh</span>
              </button>
              <AsOf iso={updated} label="Updated" />
            </div>
          </div>

          {metaPoll.error && !metaPoll.data && (
            <ErrorNote message={`${metaPoll.error}. If this persists, check that stablecoin.enabled is true in conf.yaml.`} />
          )}

          <TabBar label="Stablecoin views" tabs={TAB_CHOICES} active={view.tab} onSelect={selectTab} idPrefix="stable" />

          {TAB_CHOICES.map(t =>
            mounted[t.id] ? (
              <div
                key={t.id}
                role="tabpanel"
                id={`stable-panel-${t.id}`}
                aria-labelledby={`stable-tab-${t.id}`}
                hidden={view.tab !== t.id}
              >
                <ColdState>{t.label}: this tab is not built yet.</ColdState>
              </div>
            ) : null,
          )}

          {/* Footer: sources, BigQuery status and the disclaimer (§7.3). */}
          <div className={`space-y-2 border-t pt-3 ${cx.border}`}>
            {sources.length > 0 && (
              <div className="flex flex-wrap items-center gap-2">
                <span className={`text-[11px] font-medium ${cx.muted}`}>Sources:</span>
                <Sources sources={sources} />
              </div>
            )}
            <p className={`text-[11px] ${cx.muted}`}>
              {overviewPoll.data?.disclaimer || DISCLAIMER} BigQuery activity panels ship in v1.1; this view issues no BigQuery jobs.
            </p>
          </div>
        </div>
      </div>
    </StableThemeContext.Provider>
  );
}

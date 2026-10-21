// Deep links (stablecoin.md §7.3): asset, tab and range live in the URL query,
// parsed against the same whitelists as the API (§9). Invalid values fall back
// to the defaults. Pure functions: no DOM access, so they are unit-tested.

export type StableAsset = 'all' | 'usdt' | 'usdc' | 'eurc';
export type StableTab = 'overview' | 'peg' | 'supply' | 'reserves' | 'chains' | 'controls';
export type StableRange = '24h' | '7d' | '30d' | '90d' | '1y' | 'all';

export interface StableView {
  asset: StableAsset;
  tab: StableTab;
  range: StableRange | null; // null on tabs without a range selector
}

export const ASSETS: readonly StableAsset[] = ['all', 'usdt', 'usdc', 'eurc'];
export const TABS: readonly StableTab[] = ['overview', 'peg', 'supply', 'reserves', 'chains', 'controls'];

// The ranges each tab's endpoint accepts (§9: /overview and /supply
// 30d|90d|1y|all, /peg 24h…all) and its default. Reserves, Chains and
// Controls have no range.
export const TAB_RANGES: Record<StableTab, readonly StableRange[]> = {
  overview: ['30d', '90d', '1y', 'all'],
  peg: ['24h', '7d', '30d', '90d', '1y', 'all'],
  supply: ['30d', '90d', '1y', 'all'],
  reserves: [],
  chains: [],
  controls: [],
};

export const TAB_DEFAULT_RANGE: Record<StableTab, StableRange | null> = {
  overview: '90d',
  peg: '7d',
  supply: '90d',
  reserves: null,
  chains: null,
  controls: null,
};

const isAsset = (v: string | null): v is StableAsset => v !== null && (ASSETS as readonly string[]).includes(v);
const isTab = (v: string | null): v is StableTab => v !== null && (TABS as readonly string[]).includes(v);

// effectiveRange keeps a selection where the tab accepts it, else the tab's
// default (null for tabs without a range).
export function effectiveRange(tab: StableTab, range: string | null): StableRange | null {
  const allowed = TAB_RANGES[tab];
  if (range !== null && (allowed as readonly string[]).includes(range)) return range as StableRange;
  return TAB_DEFAULT_RANGE[tab];
}

export function parseView(search: string): StableView {
  const q = new URLSearchParams(search);
  const asset = q.get('asset');
  const tab = q.get('tab');
  const view: StableView = {
    asset: isAsset(asset) ? asset : 'all',
    tab: isTab(tab) ? tab : 'overview',
    range: null,
  };
  view.range = effectiveRange(view.tab, q.get('range'));
  return view;
}

// viewSearch returns the query string for a view, keeping unrelated
// parameters (e.g. dataset=stablecoin) in place.
export function viewSearch(view: StableView, currentSearch: string): string {
  const q = new URLSearchParams(currentSearch);
  q.set('asset', view.asset);
  q.set('tab', view.tab);
  const range = effectiveRange(view.tab, view.range);
  if (range === null) q.delete('range');
  else q.set('range', range);
  return `?${q.toString()}`;
}

// safeHref returns a URL only when it is absolute http(s); curated and
// upstream links (source_url, explorer and tx URLs) never become
// javascript:, data: or protocol-relative hrefs.
export function safeHref(url: string | null | undefined): string | undefined {
  if (!url) return undefined;
  return /^https?:\/\/[^\s/]/i.test(url) ? url : undefined;
}

// datasetHref links to another Open Data dashboard (App reads ?dataset=),
// e.g. Payment Check: datasetHref('crypto', '#pay?asset=USDT&network=tron').
export function datasetHref(dataset: string, hash = ''): string {
  return `?dataset=${encodeURIComponent(dataset)}${hash}`;
}

// Payload shapes of /api/opendata/stablecoin/* (stablecoin.md §9), mirroring
// the Go structs' JSON tags. Nullable numbers stay `number | null` and render
// "—" (§12.1).
import type { SourceStatus } from '../common/types';

export type Numeraire = 'USD' | 'EUR' | 'USDT' | string;

// --- signals ---

export interface StableSignal {
  id: string; // S1 … S13
  key: string;
  dimension: string;
  policy: 'sampled' | 'confirmed' | 'level' | string;
  state: string; // normal | info | watch | warning | critical | unknown | n/a
  reason?: string;
  rule: string;
  summary: string;
  since?: string;
  as_of?: string;
  inputs: Record<string, unknown> | null;
}

// --- /meta ---

export interface StableMetaAsset {
  id: string;
  symbol: string;
  numeraire: Numeraire;
  issuer: string;
}

export interface StableProviderHealth {
  key: string;
  label: string;
  attribution: string;
  enabled: boolean;
  status: 'ok' | 'stale' | 'error' | 'pending' | 'disabled' | 'not_in_use' | string;
  last_ok?: string;
  last_error?: string;
  success_24h_pct: number | null;
  p50_latency_ms: number | null;
}

export interface StableSamplerHealth {
  name: string;
  interval_seconds: number;
  last_run?: string;
  last_ok?: string;
  next_due?: string;
  consecutive_failures: number;
  stalled: boolean;
}

export interface StableHealthReason {
  code: string;
  asset: string;
  source: string;
  detail: string;
}

export interface StableDataHealth {
  state: string;
  checks: string[];
  reasons: StableHealthReason[];
}

export interface StableMeta {
  as_of: string;
  assets: StableMetaAsset[];
  providers: StableProviderHealth[];
  samplers: StableSamplerHealth[];
  facts: {
    version: string;
    override_status: string;
    override_version?: string;
    overridden?: Record<string, string[]>;
    asset_status: Record<string, string>;
  };
  bigquery: { jobs: boolean; status: string; initial_days: number; monthly_budget_gib: number; tron: boolean };
  coverage_notes: string[];
  data_health: StableDataHealth;
}

// --- /live ---

export interface StableLiveAsset {
  asset: string;
  symbol: string;
  numeraire: Numeraire;
  consensus: number | null;
  dev_bps: number | null;
  venues_used: number | null;
  peg_state?: string;
  peg_signal: string | null;
  dev_min_24h_bps: number | null;
  dev_max_24h_bps: number | null;
  dev_range_from?: string;
  dev_range_partial: boolean;
  price_as_of?: string;
  price_stale?: boolean;
  circulating_native: number | null;
  circulating_source?: string;
  circulating_as_of?: string;
  circulating_stale?: boolean;
  circulating_degraded?: boolean;
  net_issuance_24h_native: number | null;
  net_issuance_24h_pct: number | null;
  net_issuance_24h_source?: string;
  worst_signal: string | null;
}

export interface StableLive {
  as_of?: string;
  warming_up: boolean;
  peg_warming_up: boolean;
  assets: StableLiveAsset[];
  market_total_usd: number | null;
  market_as_of?: string;
  market_total_30d_change_pct: number | null;
  alerts: Record<string, number> | null;
}

// --- shared supply pieces ---

export interface StableNetIssuance {
  window: '24h' | '7d' | '30d' | string;
  net_native: number | null;
  net_pct: number | null;
  source: 'issuer' | 'aggregator' | '' | string;
  from?: string;
  to?: string;
}

export interface StableSupplyDay {
  day: string;
  issuer_circulating_native: number | null;
  aggregator_circulating_native: number | null;
  aggregator_circulating_usd: number | null;
  aggregator_change_native: number | null;
}

// --- /overview ---

export interface StableAttestationAge {
  period_end: string;
  published: string;
  firm: string;
  age_days: number;
  cadence_days: number;
  attestations_checked?: string;
}

export interface StableOverviewCard {
  asset: string;
  symbol: string;
  numeraire: Numeraire;
  consensus: number | null;
  dev_bps: number | null;
  price_as_of?: string;
  peg_signal: string;
  circulating_native: number | null;
  circulating_source?: string;
  circulating_as_of?: string;
  net_issuance: StableNetIssuance[];
  coverage: number | null;
  coverage_as_of?: string;
  attestation: StableAttestationAge | null;
  regulatory: Record<string, string> | null;
  control_state: string;
  control_summary: string;
  freezes_30d: number | null;
  worst_signal: string;
}

export interface StableMatrixRow {
  asset: string;
  worst: string;
  signals: StableSignal[];
}

export interface StableEvent {
  ts: string;
  asset: string;
  kind: string;
  severity: 'info' | 'watch' | 'warning' | 'critical' | string;
  title: string;
  source: string;
  url?: string;
  detail?: Record<string, unknown>;
}

export interface StableWatchRow {
  llama_id: string;
  symbol: string;
  name: string;
  peg_type: string;
  numeraire: Numeraire;
  peg_mechanism: string;
  core?: string;
  circulating_native: number | null;
  circulating_usd: number | null;
  change_1d_pct: number | null;
  change_7d_pct: number | null;
  change_30d_pct: number | null;
  price_usd: number | null;
  dev_bps: number | null;
  share_pct: number | null;
}

export interface StableWatchlist {
  as_of?: string;
  stale: boolean;
  source: string;
  fx_eur_usd: number | null;
  fx_as_of?: string;
  usd_total_usd: number | null;
  eur_total_usd: number | null;
  eur_share_pct: number | null;
  rows: StableWatchRow[];
  note: string;
}

export interface StableOverview {
  as_of: string;
  range: string;
  warming_up: boolean;
  cards: StableOverviewCard[];
  matrix: StableMatrixRow[];
  alerts: Record<string, number> | null;
  events: StableEvent[];
  supply_history: { asset: string; days: StableSupplyDay[] }[];
  watchlist: StableWatchlist;
  disclaimer: string;
}

// --- /peg ---

export interface StableQuote {
  venue: string;
  pair: string;
  numeraire: Numeraire;
  kind: 'market' | 'direct' | 'derived' | 'cross' | 'inverse' | 'dex' | string;
  legs?: string[];
  bid: number | null;
  ask: number | null;
  mid: number | null;
  spread_bps: number | null;
  vol_usd: number | null;
  age_seconds: number | null;
  last_trade_age_s?: number | null;
  weight: number | null;
  included: boolean;
  exclude_reason?: string;
}

export interface StablePegPoint {
  ts: string;
  state?: string;
  consensus: number | null;
  dev_bps: number | null;
  disp_bps?: number | null;
  venues_used?: number | null;
  min_dev_bps?: number | null;
  max_dev_bps?: number | null;
  minutes_ge_30?: number | null;
  minutes_ge_100?: number | null;
}

export interface StableDepegDay {
  day: string;
  source: string;
  close: number;
  close_dev_bps: number;
  threshold_bps: number;
  min_dev_bps?: number | null;
  max_dev_bps?: number | null;
  minutes_ge_30?: number | null;
  minutes_ge_100?: number | null;
}

export interface StableDepegMarker {
  day: string;
  source: string;
  low: number;
  low_dev_bps: number;
  close: number | null;
  confirmed_by: string[];
}

export interface StableDepegClose {
  day: string;
  source: string;
  close: number;
  dev_bps: number;
  threshold_bps: number;
}

export interface StableLastDeviation {
  day: string;
  source: string;
  dev_bps: number;
  days_since: number;
}

export interface StableAnnotation {
  date: string;
  end_date?: string;
  asset: string;
  title: string;
  extreme?: string;
  low_price?: number | null;
  status?: string;
  source_url?: string;
}

export interface StableHeatRow {
  venue: string;
  pair: string;
  kind: string;
  dev_bps: (number | null)[];
}

export interface StablePegHeatmap {
  resolution: '5m' | '1h' | string;
  buckets: string[];
  rows: StableHeatRow[];
}

export interface StablePegAsset {
  asset: string;
  numeraire: Numeraire;
  state: 'ok' | 'single_venue' | 'unknown' | string;
  consensus: number | null;
  dev_bps: number | null;
  disp_bps: number | null;
  single_venue_price: number | null;
  inputs_used: number;
  venues_used: number;
  venues: string[];
  direct_eur_venues?: number;
  second_direct_eur_venue?: boolean;
  partially_dependent?: boolean;
  quotes: StableQuote[];
  as_of: string;
  series_resolution: '5m' | '1d' | string;
  series: StablePegPoint[];
  depeg_days: StableDepegDay[];
  depeg_days_total: number;
  low_markers: StableDepegMarker[];
  last_ge_100: StableLastDeviation | null;
  history_from?: string;
  annotations: StableAnnotation[];
  daily_closes: StableDepegClose[];
  heatmap: StablePegHeatmap | null;
}

export interface StableDexPoint {
  ts: string;
  price: number;
  dev_bps: number;
}

export interface StableRelative {
  pair: string;
  median: number | null;
  dev_bps: number | null;
  exchange_median: number | null;
  uniswap: number | null;
  inputs_used: number;
  quotes: StableQuote[];
  as_of: string;
  series_resolution: string;
  series: StablePegPoint[];
  heatmap: StablePegHeatmap | null;
  uniswap_series: StableDexPoint[];
}

export interface StableFX {
  eur_usd: number | null;
  venues_used: number;
  quotes: StableQuote[];
  volume_rate: number | null;
  volume_source?: string;
}

export interface StablePeg {
  as_of?: string;
  range: string;
  warming_up: boolean;
  stale: boolean;
  assets: StablePegAsset[];
  relative: StableRelative;
  fx: StableFX | null;
  fx_single_venue: boolean;
  markets: StableQuote[];
  annotations: StableAnnotation[];
  annotations_overridden: boolean;
  depeg_threshold_bps: number;
  low_marker_bps: number;
  ecb: { day: string; rate: number } | null;
}

// --- /supply ---

export interface StableSupplyRow {
  chain: string;
  chain_label: string;
  tier?: string;
  token_label?: string;
  verified: boolean;
  unmapped?: boolean;
  issuer_circulating_native: number | null;
  issuer_unissued_native?: number | null;
  issuer_quarantined_native?: number | null;
  issuer_as_of?: string;
  issuer_share_pct: number | null;
  onchain_total_native: number | null;
  onchain_as_of?: string;
  onchain_stale?: boolean;
  inventory_native: number | null;
  aggregator_circulating_native: number | null;
  change_1d_pct: number | null;
  change_7d_pct: number | null;
  change_30d_pct: number | null;
  change_source?: string;
}

export interface StableReconOnchain {
  chain: string;
  method: string;
  issuer_native: number | null;
  onchain_native: number | null;
  excess_native: number | null;
  tolerance_native: number | null;
  issuer_as_of?: string;
  issuer_update_id?: number;
  onchain_as_of?: string;
  skew_seconds: number | null;
  aligned: boolean;
  exceeds_tolerance: boolean;
  consecutive_samples: number;
  consecutive_updates: number;
  confirmed: boolean;
  skip_reason?: string;
}

export interface StableAuthorizedCheck {
  chain: string;
  level: string;
  issuer_update_id: number;
  issuer_as_of: string;
  total_authorized_native: number | null;
  onchain_native: number | null;
  onchain_as_of?: string;
  diff_native: number | null;
  result: 'match' | 'mismatch' | 'skipped' | string;
  skip_reason?: string;
}

export interface StableReconcile {
  aggregator: {
    issuer_native: number | null;
    aggregator_native: number | null;
    diff_pct: number | null;
    issuer_as_of?: string;
    aggregator_as_of?: string;
    skew_seconds: number | null;
    watch_pct: number;
    warning_pct: number;
    above_watch: boolean;
    above_warning: boolean;
    skip_reason?: string;
  };
  onchain: StableReconOnchain[];
  onchain_as_of?: string;
  tether_authorized?: StableAuthorizedCheck[];
}

export interface StableSupplyAsset {
  asset: string;
  symbol: string;
  numeraire: Numeraire;
  totals: {
    issuer_circulating_native: number | null;
    issuer_as_of?: string;
    aggregator_circulating_native: number | null;
    aggregator_circulating_usd: number | null;
    aggregator_as_of?: string;
    inventory_native: number | null;
    inventory_chains: number;
  };
  chains: StableSupplyRow[];
  net_issuance: StableNetIssuance[];
  reconcile: StableReconcile;
  history: StableSupplyDay[];
  sources: SourceStatus[];
  unmapped_chains: { source: string; name: string }[];
  data_health: StableDataHealth;
}

export interface StableSupply {
  as_of?: string;
  range: string;
  warming_up: boolean;
  assets: StableSupplyAsset[];
}

// --- /reserves ---

export interface StableAttestation {
  period_end: string;
  published: string;
  firm: string;
  type: string;
  reserves_native?: string;
  liabilities_native?: string;
  url: string;
  note?: string;
  ratio: number | null;
  age_days: number;
}

export interface StableReserveMix {
  as_of: string;
  buckets: { id: string; share_pct: number | null }[];
  source_url: string;
  attested?: boolean;
  note?: string;
}

export interface StableIssuerEntity {
  name: string;
  jurisdiction: string;
  role: string;
  source_url: string;
  as_of: string;
}

export interface StableRedemption {
  channel: string;
  eligibility: string;
  min_native?: string;
  fee?: string;
  as_of: string;
  source_url: string;
}

export interface StableRegulatory {
  regime: string;
  status: string;
  entity?: string;
  as_of: string;
  source_url: string;
  note?: string;
}

export interface StableTetherPoint {
  update_id: number;
  as_of: string;
  total_assets_usd: number | null;
  total_liabilities_usd: number | null;
  shareholder_equity_usd: number | null;
  coverage: number | null;
}

export interface StableReservesAsset {
  asset: string;
  symbol: string;
  numeraire: Numeraire;
  facts_status: 'curated' | 'unknown' | string;
  overridden: string[];
  tether?: { latest: StableTetherPoint | null; history: StableTetherPoint[] };
  signals: StableSignal[];
  cadence_days: number;
  attestations_checked?: string;
  attestations: StableAttestation[];
  reserve_mix: StableReserveMix[];
  issuer: { name: string; entities?: StableIssuerEntity[] } | null;
  redemption: StableRedemption[];
  regulatory: StableRegulatory[];
  events: { date: string; kind: string; title: string; low_price?: number | null; source_url: string }[];
}

export interface StableRegime {
  regime: string;
  key_dates: { date: string; label: string; source_url?: string; past: boolean }[];
  note?: string;
  as_of: string;
  source_url: string;
}

export interface StableReserves {
  as_of: string;
  asset: string;
  facts_version: string;
  override_status: string;
  override_version?: string;
  assets: StableReservesAsset[];
  regimes: StableRegime[];
  regimes_overridden: boolean;
  disclaimer: string;
}

// --- /chains ---

export interface StableTransferCost {
  status: string;
  label: string;
  source: string;
  native_unit: string;
  native: number | null;
  usd: number | null;
  usd_new_address?: number | null;
  measured_at?: string;
  as_of?: string;
}

export interface StableChainRow {
  chain: string;
  chain_label: string;
  tier: string;
  token_label?: string;
  verified: boolean;
  contract?: string;
  issuer_circulating_native: number | null;
  issuer_share_pct: number | null;
  aggregator_circulating_native: number | null;
  freeze_mechanism: { kind: string; function: string; controls_monitored: boolean } | null;
  explorer_url?: string;
  verify_payment: { asset: string; network: string; hash: string } | null;
  transfer_cost: StableTransferCost | null;
}

export interface StableChains {
  as_of?: string;
  warming_up: boolean;
  assets: {
    asset: string;
    symbol: string;
    numeraire: Numeraire;
    issuer_circulating_native: number | null;
    issuer_as_of?: string;
    rows: StableChainRow[];
  }[];
  cost_note: string;
}

// --- /controls ---

export interface StableControlField {
  field: string;
  value: string | null;
  unset?: boolean;
  explorer_url?: string;
  baseline: boolean;
  first_seen?: string;
  confirmed_at?: string;
  confirmed_by?: string;
  last_seen?: string;
  last_read_at?: string;
  read_error?: string;
  endpoint?: string;
  stale: boolean;
  pending?: { value: string; first_seen: string; disputed: boolean };
}

export interface StableControlDeployment {
  asset: string;
  chain: string;
  chain_label: string;
  kind: string;
  contract: string;
  explorer_url?: string;
  monitoring_since?: string;
  last_change?: string;
  changed_last_7d: string[];
  paused: boolean | null;
  deprecated?: boolean | null;
  fields: StableControlField[];
}

export interface StableControlChange {
  detected_at: string;
  asset: string;
  chain: string;
  field: string;
  old: string;
  new: string;
  first_seen: string;
  source: string;
  confirmed_by: string;
  tx_hash?: string;
  critical: boolean;
}

export interface StableFreezeWindow {
  days: number;
  from: string;
  to: string;
  freezes: number;
  unfreezes: number;
  destroys: number;
  destroyed_native: string | null;
  partial: boolean;
}

export interface StableFreezeStats {
  asset: string;
  chain: string;
  token: string;
  source: string;
  status: 'ok' | 'not_synced' | string;
  coverage_from?: string;
  cursor?: string;
  coverage_note?: string;
  frozen_now: number | null;
  windows: StableFreezeWindow[];
  recent: { action: string; address: string; amount?: string; tx_hash: string; tx_url: string; block_time: string }[];
}

export interface StableScamChainCounters {
  status: string;
  source: string;
  from?: string;
  to?: string;
  coverage_from?: string;
  partial: boolean;
  days_with_data: number;
  poison_hits: number | null;
  poison_victims: number | null;
  counterfeit_contracts?: Record<string, number>;
  counterfeit_transfers?: number | null;
}

export interface StableControls {
  as_of?: string;
  provider_status: string;
  assets: { asset: string; symbol: string; deployments: StableControlDeployment[] }[];
  changes: StableControlChange[];
  freezes: StableFreezeStats[];
  quarantined: {
    as_of: string;
    update_id: number;
    total_native: number;
    chains: { chain: string; chain_label: string; quarantined_native: number }[];
  } | null;
  scam: { window_days: number; note: string; eth: StableScamChainCounters | null; tron: StableScamChainCounters | null };
}

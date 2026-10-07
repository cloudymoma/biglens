export interface QueryFilters {
  region: string;
  dataset: string;
  table: string;
  user_email: string;
  time_range: string;
  job_type: string;   // '' | QUERY | LOAD | EXTRACT | COPY
  status: string;     // '' | success | failed
  cache_hit: string;  // '' | hit | miss
  billing: string;    // '' | ondemand | reservation
  principal: string;  // '' | human | sa
  group_by: string;   // user | dataset | table | reservation
}

export interface StorageStats {
  logical_bytes: number;
  physical_bytes: number;
  total_bytes: number;
  active_logical?: number;
  long_term_logical?: number;
  active_physical?: number;
  long_term_physical?: number;
  time_travel?: number;
  fail_safe?: number;
}

export interface StorageBreakdown {
  active_bytes: number;
  long_term_bytes: number;
}

export interface TopTable {
  dataset: string;
  table_name: string;
  total_bytes: number;
}

export interface SearchIndexInfo {
  dataset: string;
  table_name: string;
  index_name: string;
  index_status: string;
  coverage_percentage: number;
  total_logical_bytes: number;
  total_storage_bytes: number;
}

export interface DatasetStorage {
  dataset: string;
  billing_model?: string;
  active_logical: number;
  long_term_logical: number;
  active_physical: number;   // includes time-travel bytes (BQ semantics)
  long_term_physical: number;
  time_travel: number;
  fail_safe: number;
}

export interface ColdTable {
  dataset: string;
  table_name: string;
  total_bytes: number;
  active_logical?: number;
  long_term_logical?: number;
  active_physical?: number;
  long_term_physical?: number;
  billing_model?: string;
  storage_tier: string; // ACTIVE | LONG_TERM
}

export interface StorageDashboardData {
  billing: StorageStats | null;
  breakdown: StorageBreakdown | null;
  top_tables: TopTable[] | null;
  search_indexes: SearchIndexInfo[] | null;
  dataset_storage: DatasetStorage[] | null;
  cold_tables: ColdTable[] | null;
  degraded_widgets?: string[];
}

// One slot-timeline bucket. avg_* spread the bucket's slot-ms over its full
// length (idle seconds count as zero); peak_* are the busiest single second
// inside the bucket.
export interface SlotBucket {
  bucket_start: string;
  avg_running: number;
  avg_pending: number;
  peak_total: number; // RUNNING + PENDING
  peak_pending: number;
}

export interface TopSlotJob {
  job_id: string;
  user_email: string;
  total_slot_ms: number;
  duration_ms: number;
  state: string;
  cache_hit: boolean;
  reservation: string;
}

export interface SlotUsage {
  usage_hour: string;
  avg_slots: number;
}

export interface QueueStats {
  avg_queue_ms: number;
  p95_queue_ms: number;
  avg_run_ms: number;
  p95_run_ms: number;
  job_count: number;
}

export interface ReservationPoint {
  period_start: string;
  assigned: number;
  autoscale: number;
}

export interface ComputeDashboardData {
  slot_timeline: SlotBucket[] | null;
  slot_bucket_seconds: number; // width of each slot_timeline bucket
  top_jobs: TopSlotJob[] | null;
  slot_usage: SlotUsage[] | null;
  queue_stats: QueueStats | null;
  reservations: ReservationPoint[] | null;
  degraded_widgets?: string[];
}

export interface CostSummary {
  bytes_billed: number;
  ondemand_bytes_billed?: number;
  bytes_processed: number;
  total_slot_ms: number;
}

export interface SpendEntry {
  name: string;
  total_bytes: number;
}

export interface DailyCost {
  day: string;
  bytes_billed: number;
}

export interface CostDashboardData {
  summary: CostSummary | null;
  spend_by: SpendEntry[] | null;
  daily_cost: DailyCost[] | null;
}

export interface Recommendation {
  recommender: string;
  description: string;
  category: string;
  projected_savings_usd: number;
}

export interface ErrorStat {
  reason: string;
  job_count: number;
  slot_ms: number;
}

export interface FailingUser {
  user_email: string;
  job_count: number;
  slot_ms: number;
}

export interface PerfInsightJob {
  job_id: string;
  user_email: string;
  slot_ms: number;
  slot_contention: boolean;
  shuffle_quota: boolean;
  high_card_join: boolean;
  partition_skew: boolean;
}

export interface RepeatedQuery {
  query_hash: string;
  sample_query: string;
  runs: number;
  total_bytes: number;
  user_count: number;
}

export interface InsightsDashboardData {
  recommendations: Recommendation[] | null;
  error_stats: ErrorStat[] | null;
  failing_users: FailingUser[] | null;
  perf_insights: PerfInsightJob[] | null;
  repeated_queries: RepeatedQuery[] | null;
  degraded_widgets?: string[];
}

export interface JobRow {
  job_id: string;
  user_email: string;
  job_type: string;
  statement_type: string;
  state: string;
  error_reason: string;
  creation_time: string;
  reservation: string;
  queue_ms: number;
  duration_ms: number;
  slot_ms: number;
  bytes_billed: number;
  cache_hit: boolean;
  ref_tables: string[] | null;
  query: string;
  slot_contention: boolean;
  shuffle_quota: boolean;
  high_card_join: boolean;
  partition_skew: boolean;
}

export interface JobsDashboardData {
  jobs: JobRow[] | null;
}

// --- IAM Security ---

export interface IAMSummary {
  total_emails: number;
  service_accounts: number;
  human_users: number;
  total_calls: number;
}

export interface UsageTimepoint {
  bucket: string;
  email: string;
  call_count: number;
}

export interface TopCaller {
  email: string;
  total_calls: number;
  total_slot_ms: number;
  total_bytes: number;
  avg_duration_sec: number;
  last_active: string;
}

export interface InactiveEmail {
  email: string;
  last_active: string;
  days_idle: number;
  total_calls: number;
}

export type GranteeKind = 'user' | 'serviceAccount' | 'group' | 'domain' | 'projectRole' | 'deleted' | 'special' | 'public';

export interface PublicFlag {
  dataset: string;
  object_type: string;
  role: string;
  grantee: string;
  kind: GranteeKind;
}

export interface PrincipalGrant {
  principal: string;
  kind: GranteeKind;
  datasets: string[];
  roles: string[];
  write_capable: boolean;
}

export interface ProjectBinding {
  role: string;
  basic: boolean;
  members: string[];
}

export interface DatasetPosture {
  dataset: string;
  cmek: boolean;
  kms_key: string;
  default_exp_days: number;
}

export interface RLSPolicy {
  dataset: string;
  table: string;
  policy: string;
  predicate: string;
  modified: string;
}

// Coverage of the row access policy scan (RowAccessPolicies.List per table).
// Only 'complete' lets an empty rls_policies list mean "no policies".
export interface RLSScan {
  status: 'complete' | 'partial' | 'not_evaluated';
  tables_checked: number;
  tables_total: number;
  datasets_failed: number;
  truncated: boolean;
  timed_out: boolean;
  max_tables: number;
}

export interface SensitiveColumn {
  dataset: string;
  table: string;
  column: string;
  tagged: boolean;
}

export interface SecurityDashboardData {
  public_flags: PublicFlag[] | null;
  principals: PrincipalGrant[] | null;
  unused_grants: PrincipalGrant[] | null;
  project_bindings: ProjectBinding[] | null;
  tag_bypassers: string[] | null;
  project_iam_error: string;
  dataset_posture: DatasetPosture[] | null;
  rls_policies: RLSPolicy[] | null;
  rls_scan?: RLSScan | null;
  sensitive_columns: SensitiveColumn[] | null;
  untagged_sensitive_total?: number;
  datasets_scanned: number;
  datasets_total: number;
  grants_datasets_failed?: number;
  degraded_widgets?: string[];
}

export interface NewActor {
  email: string;
  first_seen: string;
  prior_active?: string;
  jobs: number;
  is_sa: boolean;
}

export interface OffHoursCell { dow: number; hr: number; jobs: number }
export interface OffHoursUser { email: string; jobs: number }

export interface ExfilSignal {
  email: string;
  job_id: string;
  signal: 'EXTRACT_TO_GCS' | 'EXPORT_DATA' | 'CROSS_PROJECT_WRITE' | 'LARGE_SCAN';
  bytes: number;
  dest_project: string;
  created: string;
}

export interface IAMDashboardData {
  summary: IAMSummary | null;
  timeline: UsageTimepoint[] | null;
  top_callers: TopCaller[] | null;
  inactive_7d: InactiveEmail[] | null;
  inactive_30d: InactiveEmail[] | null;
  inactive_90d: InactiveEmail[] | null;
  new_actors: NewActor[] | null;
  off_hours: OffHoursCell[] | null;
  off_hours_top: OffHoursUser[] | null;
  exfil_signals: ExfilSignal[] | null;
  degraded_widgets?: string[];
}

// --- Dataplex / Knowledge Catalog (OKF) ---

export interface GraphNode {
  id: string;
  title: string;
  type: string;
  description: string;
  resource: string;
  fqn?: string;
  user_managed?: boolean;
  tags: string[] | null;
}

export interface GraphEdge {
  source: string;
  target: string;
  kind?: string; // containment | lineage | definition | reference
}

export interface CatalogGraph {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

export interface Concept {
  id: string;
  type: string;
  title: string;
  description: string;
  resource: string;
  fqn?: string;
  user_managed?: boolean;
  tags: string[] | null;
  timestamp: string;
  body: string;
  links: string[] | null;
}

export interface ConceptDetail {
  concept: Concept;
  neighbors: GraphNode[] | null;
}

export interface CatalogTypeCount {
  type: string;
  count: number;
}

export interface CatalogManifest {
  project: string;
  location: string;
  query: string;
  lineage_location?: string;
  imported_at: string;
  truncated?: boolean;
  entry_type_counts?: Record<string, number>;
}

export interface ImportResult {
  imported: number;
  edges: number;
  containment_edges: number;
  lineage_edges: number;
  lineage_dropped: number;
  definition_edges: number;
  definition_dropped: number;
  definition_error?: string;
  duplicate_entries?: number;
  id_collisions?: number;
  preserved: number;
  pruned: number;
  truncated: boolean;
  lineage_error?: string;
  prune_error?: string;
  aspect_error?: string;
  aspect_failed?: number;
  elapsed_ms?: number;
  type_counts: Record<string, number>;
}

// --- BigQuery Open Data: Google Trends ---

export interface TrendsCountry {
  name: string;
  code: string;
}

export interface TrendsMeta {
  latest_refresh_date: string;
  refresh_dates: string[];
  countries: TrendsCountry[];
  // US market dates come from the DMA tables, which publish separately.
  us_latest_refresh_date: string;
  us_refresh_dates: string[];
  dmas: SemDMA[];
}

export interface TrendsTopTerm {
  term: string;
  rank: number;
  score: number;
}

export interface TrendsRisingTerm {
  term: string;
  rank: number;
  percent_gain: number;
  score: number;
}

export interface TrendsDashboardData {
  top_terms: TrendsTopTerm[];
  rising_terms: TrendsRisingTerm[];
}

export interface TrendsGeoPoint {
  country_code: string;
  country_name: string;
  score: number;
  rank: number;
}

export interface TrendsHistoryPoint {
  term: string;
  week: string;
  score: number;
}

export interface TrendsTermData {
  geo: TrendsGeoPoint[];
  history: TrendsHistoryPoint[];
}

// --- BigQuery Open Data: SEM Insights ---

export type SemMarket = 'global' | 'us';

export interface SemDMA {
  name: string;
  id: number;
}

export interface SemMeta {
  latest_refresh_date: string;
  refresh_dates: string[];
  countries: TrendsCountry[];
  dmas: SemDMA[];
}

export interface SemMatrixRow {
  term: string;
  volume_rank: number; // 0 = not charting in the top 25 ("Unranked")
  percent_gain: number;
  score: number; // 0 = too new to score
  geo_spread: number;
  rising_rank: number;
}

export interface SemDashboardData {
  matrix: SemMatrixRow[];
}

export interface SemGeoRow {
  geo: string; // DMA name (us) or region name (global)
  // vs. this geo's own 5-year peak (not comparable across geos);
  // null = insufficient data (below the Trends reporting threshold)
  score: number | null;
  rising_rank: number; // 0 = not rising in this geo
  percent_gain: number;
}

export interface SemGeoData {
  week: string; // Sunday starting the snapshot's latest complete week ('' = no rows)
  rows: SemGeoRow[];
}

export interface SemPulseRow {
  term: string;
  rank: number;
  score: number; // current (partial) week
  prev_week_score: number; // 0 = no prior week in the snapshot's history
}

export interface SemPulseData {
  snapshot_time: string;
  rows: SemPulseRow[];
}

export interface SemHistoryPoint {
  week: string;
  score: number;
}

export interface SemTermData {
  history: SemHistoryPoint[];
}

export interface SemSafetyRow {
  ingest_date: string;
  event_count: number;
  avg_tone: number;
  conflict_share: number; // fraction of CAMEO QuadClass 3/4 events
}

export interface SemSafetyData {
  rows: SemSafetyRow[];
}

// --- BigQuery Open Data: GDELT ---

export interface GdeltOverall {
  event_count: number;
  avg_tone: number;
  avg_goldstein: number;
}

export interface GdeltDaily {
  ingest_date: string;
  event_count: number;
  avg_tone: number;
}

export interface GdeltQuadClass {
  quad_class: number;
  event_count: number;
}

export interface GdeltEventType {
  event_root_code: string;
  event_count: number;
  avg_goldstein: number;
  avg_tone: number;
}

export interface GdeltHotspot {
  latitude: number;
  longitude: number;
  fips_country: string;
  event_count: number;
  avg_tone: number;
}

export interface GdeltNews {
  ingest_date: string;
  fips_country: string;
  event_root_code: string;
  avg_tone: number;
  source_url: string;
  mention_count: number;
}

export interface GdeltEventsData {
  overall: GdeltOverall;
  daily: GdeltDaily[];
  quad_class: GdeltQuadClass[];
  event_types: GdeltEventType[];
  hotspots: GdeltHotspot[];
  conflict_news: GdeltNews[];
}

export interface GdeltNamedCount {
  name: string;
  article_count: number;
}

export interface GdeltMediaSource {
  media_source: string;
  article_count: number;
  avg_tone: number;
}

export interface GdeltGkgData {
  themes: GdeltNamedCount[];
  persons: GdeltNamedCount[];
  sources: GdeltMediaSource[];
}

// Country & Relations tab. Country codes are CAMEO 3-letter actor codes.

export interface GdeltDyadRow {
  country_a: string;
  country_b: string;
  event_count: number;
  avg_goldstein: number;
  avg_tone: number;
}

export interface GdeltCountryCount {
  country: string;
  event_count: number;
}

export interface GdeltDyadsData {
  dyads: GdeltDyadRow[];
  countries: GdeltCountryCount[];
}

export interface GdeltCountryDaily {
  ingest_date: string;
  event_count: number;
  avg_tone: number;
  avg_goldstein: number;
}

export interface GdeltCountryEventType {
  event_code: string;
  event_count: number;
  avg_goldstein: number;
}

export interface GdeltPartnerRow {
  partner: string;
  event_count: number;
  avg_goldstein: number;
  avg_tone: number;
}

export interface GdeltCountryEvent {
  ingest_date: string;
  actor1: string;
  actor2: string;
  event_code: string;
  goldstein: number;
  avg_tone: number;
  mention_count: number;
  source_count: number;
  source_url: string;
}

export interface GdeltCountryData {
  country: string;
  daily: GdeltCountryDaily[];
  event_types: GdeltCountryEventType[];
  partners: GdeltPartnerRow[];
  top_events: GdeltCountryEvent[];
}

// Human Impact tab: article counts of media-reported figures, never sums.

export interface GdeltImpactDaily {
  ingest_date: string;
  count_type: string;
  article_count: number;
}

export interface GdeltImpactCountry {
  fips_country: string;
  article_count: number;
}

export interface GdeltImpactIncident {
  count_type: string;
  num: number;
  location: string;
  article_count: number;
  sample_url: string;
}

export interface GdeltImpactData {
  daily: GdeltImpactDaily[];
  countries: GdeltImpactCountry[];
  incidents: GdeltImpactIncident[];
}

// Story Velocity tab: events ranked by distinct-outlet spread.

export interface GdeltStoryRow {
  mentions: number;
  outlets: number;
  avg_confidence: number;
  avg_tone: number;
  first_seen: string;
  span_minutes: number;
  actor1: string;
  actor2: string;
  event_code: string;
  location: string;
  source_url: string;
}

export interface GdeltStoriesData {
  stories: GdeltStoryRow[];
}

// Industry Pulse tab: GKG theme-filtered vertical view.
export type GdeltIndustryKey =
  | 'finance' | 'retail' | 'biomedical' | 'education' | 'technology'
  | 'transport' | 'energy' | 'agriculture' | 'tourism' | 'defense' | 'realestate';

export interface GdeltIndustryDaily {
  ingest_date: string;
  article_count: number;
  avg_tone: number;
}

export interface GdeltIndustryOrg {
  name: string;
  article_count: number;
  avg_tone: number;
}

export interface GdeltIndustryArticle {
  ingest_date: string;
  url: string;
  source: string;
  tone: number;
}

export interface GdeltIndustryData {
  daily: GdeltIndustryDaily[];
  orgs: GdeltIndustryOrg[];
  subtopics: GdeltNamedCount[];
  outlets: GdeltMediaSource[];
  articles: GdeltIndustryArticle[];
}

// --- BigQuery Open Data: NOAA GHCN-Daily weather ---

export interface WeatherMeta {
  latest_date: string;
  // Freshest day with settled station coverage — the newest 1-2 days can be
  // nearly empty while GHCN backfills, so dashboards default to this.
  default_date: string;
}

// Metric fields are null when the station did not report that element.
export interface WeatherStation {
  name: string;
  state: string;
  country: string;
  latitude: number;
  longitude: number;
  tmax_c: number | null;
  tmin_c: number | null;
  prcp_mm: number | null;
  snow_mm: number | null;
}

export interface WeatherExtreme {
  station: string;
  country_state: string;
  value: number;
}

export interface WeatherOverall {
  stations_reporting: number;
  hottest: WeatherExtreme | null;
  coldest: WeatherExtreme | null;
  wettest: WeatherExtreme | null;
  snow_stations: number;
}

export interface WeatherDaily {
  date: string;
  avg_tmax_c: number | null;
  avg_tmin_c: number | null;
  avg_prcp_mm: number | null;
  tmax_stations: number;
  prcp_stations: number;
}

export interface WeatherDashboardData {
  snapshot_date: string;
  overall: WeatherOverall;
  stations: WeatherStation[];
  daily: WeatherDaily[];
}

// --- BigQuery Open Data: Crypto Pulse ---

export interface CryptoActivityRow {
  date: string;
  tx_count: number;
  value_settled: number;
  fees_total: number;
}

export interface CryptoAddressRow {
  date: string;
  active_addresses: number;
}

export interface CryptoBlockRow {
  date: string;
  blocks: number;
  fullness_pct: number;
}

export interface CryptoKpi {
  date: string;
  tx_count: number;
  value_settled: number;
  fees_total: number;
  blocks: number;
  fullness_pct: number;
}

export interface CryptoChainPulse {
  daily: CryptoActivityRow[];
  addresses: CryptoAddressRow[];
  blocks: CryptoBlockRow[];
  kpi: CryptoKpi;
}

export interface CryptoPulseData {
  days: number;
  btc: CryptoChainPulse;
  eth: CryptoChainPulse;
}

export interface BtcFeeRow {
  date: string;
  median_fee_vb: number;
  total_fees_btc: number;
  subsidy_btc: number;
}

export interface EthFeeRow {
  date: string;
  avg_gas_gwei: number;
  total_fees_eth: number;
  burned_eth: number;
  tips_eth: number;
}

export interface CryptoFeesData {
  days: number;
  btc: BtcFeeRow[];
  eth: EthFeeRow[];
  btc_blocks: CryptoBlockRow[];
  eth_blocks: CryptoBlockRow[];
}

export type CryptoChain = 'btc' | 'eth';

export interface WhaleTx {
  hash: string;
  time: string;
  from: string;
  to: string;
  amount: number;
  from_risk?: string[]; // local Address Risk lists (ETH only)
  to_risk?: string[];
}

export interface WhaleAddress {
  address: string;
  total: number;
  tx_count: number;
  risk?: string[];
}

export interface WhaleTrendRow {
  date: string;
  whale_count: number;
}

export interface ConcentrationRow {
  date: string;
  top1pct_share: number;
}

export interface CryptoWhalesData {
  days: number;
  chain: CryptoChain;
  threshold: number;
  largest: WhaleTx[];
  top_receivers: WhaleAddress[];
  trend: WhaleTrendRow[];
  concentration: ConcentrationRow[];
  freeze_coverage_from?: string;
}

export interface TokenRow {
  token_address: string;
  symbol: string;
  name: string;
  transfers: number;
  senders: number;
  receivers: number;
}

export interface TokenDailyRow {
  date: string;
  transfers: number;
  native_txs: number;
}

export interface ContractRow {
  date: string;
  contracts: number;
  erc20: number;
  erc721: number;
}

export interface CryptoTokensData {
  days: number;
  top_tokens: TokenRow[];
  daily: TokenDailyRow[];
  contracts: ContractRow[];
}

export interface BtcMiningRow {
  date: string;
  blocks: number;
  hashrate_ehs: number;
  revenue_btc: number;
}

export interface CryptoMiningData {
  days: number;
  daily: BtcMiningRow[];
}

export interface CryptoSpotData {
  price_usd: number;
  as_of: string;
  source: string;
}

// --- Crypto Pulse: Address Risk (field names mirror backend/address_risk_lookup.go) ---

export type AddressRiskChain = 'eth' | 'arb' | 'op' | 'base' | 'tron' | 'btc';
export type AddressRiskStatus = 'ok' | 'partial' | 'stale' | 'empty' | 'error' | 'not_configured';
export type AddressRiskSeverity = 'critical' | 'warning' | 'association' | 'info';

export interface AddressRiskClue {
  severity: AddressRiskSeverity;
  source: string;
  token: string;
  flag: string;
  code: string;
  title: string;
  detail: string;
  observed_at: string | null;
  as_of: string;
  ref_url: string;
  association?: AddressRiskAssociation;
}

export interface AddressRiskAssociation {
  counterparty: string;
  counterparty_sources: string[];
  tx_count: number;
  channels: string[];
  direction: 'in' | 'out' | 'both';
  tx_hash: string;
  amount: string;
}

export interface AddressRiskListScope {
  n: number;
  oldest_at: string;
}

export interface AddressRiskScope {
  txlist: AddressRiskListScope;
  tokentx: AddressRiskListScope;
  txlistinternal: AddressRiskListScope;
  hops: number;
  token_allowlist: string[];
  counterparties_screened: number;
  counterparty_errors?: number;
  truncated: boolean;
}

export interface AddressRiskSource {
  id: string;
  status: AddressRiskStatus;
  error?: string;
  last_ok_at?: string;
  upstream_changed_at?: string;
  coverage_from?: string;
  cursor?: string;
  last_error?: string;
  hosts?: string[];
  sends_address: boolean;
  signup_url?: string;
  help_url?: string;
}

export interface AddressRiskSummary {
  counts: { critical: number; warning: number; association: number; info: number };
  checked: string[];
  failed: string[];
  incomplete: string[];
  skipped: string[];
  text: string;
}

export interface AddressRiskLookup {
  chain: AddressRiskChain;
  address: string;
  checksum_warning: boolean;
  queried_at: string;
  summary: AddressRiskSummary;
  disclaimer: string;
  clues: AddressRiskClue[];
  sources: AddressRiskSource[];
  association_scope: AddressRiskScope | null;
}

export interface AddressRiskKeyInfo {
  configured: boolean;
  hint: string;
  credits_available?: number;
}

export interface AddressRiskSources {
  lists: AddressRiskSource[];
  rpc_hosts: string[];
  goplus_host: string;
  etherscan: AddressRiskKeyInfo;
}

export interface AddressRiskTrendPoint {
  bucket: string;
  usdt_freeze: number;
  usdc_freeze: number;
  unfreeze: number;
}

export interface AddressRiskEvent {
  tx_hash: string;
  token: 'USDT' | 'USDC';
  action: 'freeze' | 'unfreeze' | 'destroy';
  address: string;
  amount: string;
  block_number: number;
  block_time: string;
}

export interface AddressRiskOverview {
  empty: boolean;
  coverage: { coverage_from: string; cursor: string; partial: boolean };
  kpis: {
    ofac_count: number;
    usdt_frozen_count: number;
    usdc_frozen_count: number;
    usdt_destroyed_total: string;
    mew_darklist_count: number;
  };
  trend: { granularity: 'day' | 'month'; points: AddressRiskTrendPoint[] };
  recent_events: AddressRiskEvent[];
}

export interface AddressRiskBackfillStatus {
  status: 'idle' | 'running' | 'partial' | 'completed' | 'failed' | 'corrupted';
  completed: boolean;
  running: boolean;
  corrupted: boolean;
  corrupt_reason?: string;
  can_run: boolean;
  since_date?: string;
  through_date?: string;
  coverage_from?: string;
  coverage_to?: string;
  events_stored: number;
  live_events: number;
  usdt_events: number;
  usdc_events: number;
  bytes_billed: number;
  estimated_usd: number;
  dry_run_ready?: boolean;
  dry_run_bytes?: number;
  dry_run_usd?: number;
  dry_run_batches?: number;
  dry_run_token?: string; // only in the POST dry_run response; confirm must send it back
  started_at?: string;
  completed_at?: string;
  progress_label?: string;
  error?: string;
}

// --- BigQuery Open Data: GCP Billing ---

export interface BillingDatasetInfo {
  dataset: string;
  has_standard: boolean;
  has_resource: boolean;
  has_pricing: boolean;
  billing_accounts: string[];
  currency: string;
  error?: string;
}

export interface BillingConfigResponse {
  datasets: BillingDatasetInfo[];
}

export interface BillingMeta {
  dataset: BillingDatasetInfo;
  projects: { id: string; name: string }[];
  services: string[];
  label_keys: string[];
  invoice_months: string[];
}

// Filter state shared by every billing tab. invoiceMonth !== '' switches the
// backend into invoice-reconciliation mode.
export interface BillingFilterState {
  dataset: string;
  start: string; // YYYY-MM-DD
  end: string;
  invoiceMonth: string;
  accounts: string[];
  projects: string[];
  services: string[];
  labelKey: string;
  labelValue: string;
}

export interface BillingKpi {
  currency: string;
  gross: number;
  net: number;
  credits: number;
  projects: number;
  services: number;
}

export interface BillingDaily {
  date: string;
  gross: number;
  net: number;
}

export interface BillingGroup {
  name: string;
  gross: number;
  net: number;
  credits: number;
}

export interface BillingOverviewData {
  kpis: BillingKpi[];
  daily: BillingDaily[];
  top_services: BillingGroup[];
  top_projects: BillingGroup[];
  projected_month_net: number | null;
}

export interface BillingSku {
  sku_id: string;
  sku: string;
  pricing_unit: string;
  usage: number;
  gross: number;
  net: number;
  effective_price: number | null;
}

export interface BillingServicesData {
  services: BillingGroup[];
  skus: BillingSku[];
  service: string;
}

export interface BillingProjectRow {
  id: string;
  name: string;
  gross: number;
  net: number;
  credits: number;
}

export interface BillingProjectsData {
  projects: BillingProjectRow[];
  label_groups: BillingGroup[];
  group_key: string;
}

export interface BillingCreditRow {
  type: string;
  name: string;
  amount: number;
}

export interface BillingCreditsData {
  credits: BillingCreditRow[];
  by_service: BillingGroup[];
}

export interface BillingResourceRow {
  id: string; // grouping key: global_name, or name when the export row has none
  name: string;
  global_name: string;
  service: string;
  project: string;
  net: number;
}

export interface BillingResourcesData {
  available: boolean;
  resources: BillingResourceRow[];
  // Window net on rows with no resource name or global name (never listed),
  // and the window's whole net; the search box narrows neither.
  unattributed_net: number;
  total_net: number;
}

export interface BillingPriceRow {
  sku_id: string;
  sku: string;
  service: string;
  pricing_unit: string;
  currency?: string;
  list_price: number;
  contract_price: number | null;
  discount_pct: number | null;
  tiers: number;
}

export interface BillingPricingData {
  available: boolean;
  as_of: string;
  currency?: string;
  prices: BillingPriceRow[];
}

// ---- GCP Resources section ----

export interface ResProjectInfo {
  project: string;
  error?: string;
}

export interface ResConfigResponse {
  projects: ResProjectInfo[];
}

export interface ResNamedCount {
  name: string;
  count: number;
}

export interface ResAssetItem {
  name: string;
  asset_type: string;
  display_name: string;
  location: string;
  state: string;
  labels?: Record<string, string>;
  created: string;
  updated: string;
}

export interface ResOverviewData {
  fetched_at: string;
  total_resources: number;
  vms_running: number;
  vms_stopped: number;
  buckets: number;
  vpcs: number;
  firewall_rules: number;
  by_service: ResNamedCount[];
  by_location: ResNamedCount[];
  recent: ResAssetItem[];
  truncated: boolean;
  partial_errors?: Record<string, string>;
}

export interface ResVMInstance {
  name: string;
  zone: string;
  machine_type: string;
  status: string;
  workload: string;
  vcpus: number;
  memory_gb: number;
  labels?: Record<string, string>;
  created: string;
}

export interface ResDiskInfo {
  name: string;
  zone: string;
  type: string;
  size_gb: number;
  users?: string[];
  created: string;
}

export interface ResComputeData {
  fetched_at: string;
  instances: ResVMInstance[];
  disks: ResDiskInfo[];
}

export interface ResBucketInfo {
  name: string;
  location: string;
  storage_class: string;
  uniform_access: boolean;
  public_access_prevention: string;
  created: string;
  bytes_by_class?: Record<string, number>;
}

export interface ResStorageData {
  fetched_at: string;
  buckets: ResBucketInfo[];
}

export interface ResVPCInfo {
  name: string;
  auto_create: boolean;
}

export interface ResSubnetInfo {
  name: string;
  region: string;
  network: string;
  cidr: string;
  private_google_access: boolean;
}

export interface ResAddressInfo {
  name: string;
  region: string;
  address: string;
  type: string;
  purpose?: string;
  status: string;
  users?: string[];
}

export interface ResFirewallInfo {
  name: string;
  network: string;
  direction: string;
  action?: string;
  priority: number;
  source_ranges?: string[];
  allowed?: string[];
  denied?: string[];
  target_tags?: string[];
  disabled: boolean;
}

export interface ResForwardingRuleInfo {
  name: string;
  region: string;
  ip_address: string;
  scheme: string;
  target: string;
  ports: string;
}

export interface ResNetworkData {
  fetched_at: string;
  networks: ResVPCInfo[];
  subnets: ResSubnetInfo[];
  addresses: ResAddressInfo[];
  firewalls: ResFirewallInfo[];
  forwarding_rules: ResForwardingRuleInfo[];
}

export interface ResExplorerData {
  fetched_at: string;
  items: ResAssetItem[];
  truncated: boolean;
}

export interface ResFinding {
  severity: 'high' | 'medium' | 'low';
  category: string;
  resource: string;
  location: string;
  summary: string;
}

export interface ResInsightsData {
  fetched_at: string;
  findings: ResFinding[];
  partial_errors?: Record<string, string>;
}

// ---- Pricing calculator (/api/calculator/*) ----

export interface StorageRates {
  active_logical: number;
  long_term_logical: number;
  active_physical: number;
  long_term_physical: number;
}

export interface StorageRegionPreset {
  region: string;
  label: string;
  rates: StorageRates; // $/GiB/month
}

export interface EditionPreset {
  edition: string;
  label: string;
  payg: number; // $/slot-hour
  commit: Record<string, number>; // term -> $/slot-hour; empty when unavailable
  max_slots?: number;
  baseline: boolean;
}

export interface CalculatorPresets {
  storage_regions: StorageRegionPreset[];
  editions: EditionPreset[];
  free_tier_gib: number;
  hours_per_month: number;
  slot_increment: number;
}

export type StorageUnit = 'GiB' | 'TiB' | 'PiB';

export interface StorageVolumes {
  active_logical: number;
  long_term_logical: number;
  active_physical: number;
  long_term_physical: number;
  fail_safe: number;
}

export interface StorageCalcRequest {
  unit: StorageUnit;
  volumes: StorageVolumes;
  rates: StorageRates;
  discount_pct: number;
  free_tier: boolean;
}

export interface StorageModelCost {
  active: number;
  long_term: number;
  fail_safe: number;
  total: number;
}

export interface StorageEstimate {
  logical: StorageModelCost;
  physical: StorageModelCost;
  cheaper: 'logical' | 'physical';
  savings: number;
  annual_recommended: number;
  data_compression_ratio: number;
  effective_compression_ratio: number;
  break_even_ratio: number;
}

export interface PeakWindow {
  id: string;
  start: string; // "HH:MM"
  end: string;   // "HH:MM"; end <= start wraps past midnight
  slots: number;
}

export interface SlotsCalcRequest {
  edition: string;
  baseline_slots: number;
  committed_slots: number;
  windows: PeakWindow[];
  payg_rate: number;
  commit_rate: number;
  discount_pct: number;
}

export interface SlotCost {
  committed: number;
  uncommitted_baseline: number;
  autoscaled: number;
  payg: number;
  total: number;
}

export interface SlotProfile {
  baseline: number[];
  windows: number[][];
  demand: number[];
  billed: number[];
}

export interface SweepPoint {
  committed: number;
  monthly: number;
}

export interface SlotsEstimate {
  profile: SlotProfile;
  hourly_cost: SlotCost[];
  daily: SlotCost;
  monthly: SlotCost;
  effective_baseline: number;
  effective_committed: number;
  peak_slots: number;
  avg_slots: number;
  committed_slot_hours: number;
  used_committed_slot_hours: number;
  payg_slot_hours: number;
  commit_utilization: number;
  sweep: SweepPoint[];
  best_commit?: SweepPoint;
  warnings: string[];
}

// --- Crypto Pulse: 72h Gas Pulse (/api/opendata/crypto/gas-pulse) ---

export interface GasChainMeta {
  id: string;
  name: string;
  primary_label: string;
  primary_unit: string;
  band_label: string; // '' = no shaded band
  load_label: string;
  load_unit: string;
  fee_unit: string;
  fee_label?: string;
  note: string;
}

export interface GasHourRow {
  hour_utc: string; // "YYYY-MM-DD HH:00"
  tx_count: number | null;
  band_low: number | null;
  primary_val: number | null;
  band_high: number | null;
  load_val: number | null;
  total_fee: number | null;
}

export interface GasStats {
  samples: number; // 0 = no data in the window
  latest_hour: string;
  latest: number;
  percentile: number;
  min_value: number;
  min_hour: string;
  max_value: number;
  max_hour: string;
  median: number;
  p90: number;
  load_max: number;
  load_max_hour: string;
  total_fee: number;
  tx_count: number;
}

export interface GasAllTime {
  unit: string; // 'gwei' | 'sun'
  ath_value: number;
  ath_time: string; // RFC3339 UTC; '' = genesis
  atl_value: number;
  atl_time: string; // first time reached
  atl_is_floor: boolean;
  current_value: number | null;
  current_time?: string;
}

export interface GasPulseChain {
  meta: GasChainMeta;
  hours: GasHourRow[];
  stats: GasStats;
  error?: string;
  all_time: GasAllTime | null;
  all_time_error?: string;
}

export interface GasPulseData {
  window_start: string;
  window_end: string;
  chains: GasPulseChain[];
}

// --- Crypto Pulse: live bars (/api/opendata/crypto/gas-live) ---

export interface BtcFeeTier {
  label: string;
  sat_vb: number; // precise, may be < 1
  usd: number | null; // cost of a standard_tx_vb transfer; null when spot is unavailable
}

export interface BtcFeeBand {
  label: string;
  min_sat_vb: number;
  vsize_mb: number;
}

export interface BtcProjectedBlock {
  median_fee: number;
  min_fee: number;
  max_fee: number;
  vsize_mb: number;
  tx_count: number;
  total_fee_btc: number;
}

export interface BtcMinedBlock {
  height: number;
  mined_at: string; // RFC3339 UTC
  median_fee: number;
  size_mb: number;
  fullness_pct: number;
  tx_count: number;
  total_fee_btc: number;
}

export interface BtcLive {
  tiers: BtcFeeTier[];
  minimum_sat_vb: number;
  tx_count: number;
  vsize_mb: number;
  blocks_to_clear: number;
  total_fee_btc: number;
  bands: BtcFeeBand[]; // ascending fee; empty when the histogram is empty
  standard_tx_vb: number;
  projected: BtcProjectedBlock[]; // next to be mined first, up to 3
  recent: BtcMinedBlock[]; // newest first, up to 5
}

export interface TronTransferCost {
  label: string;
  energy: number;
  bandwidth: number;
  burn_trx: number;
  burn_usd: number | null;
  share_pct: number; // share of USDT transfers in the last 24h
}

export interface TronLive {
  energy_price_sun: number;
  energy_price_since: string;
  bandwidth_price_sun: number;
  costs: TronTransferCost[];
  note: string;
}

export interface L2ActionCost {
  label: string;
  gas: number;
  source: string; // where the gas figure comes from
  exec_eth: number;
  l1_eth: number;
  total_eth: number;
  total_usd: number | null;
  l1_share_pct: number;
  savings_pct: number | null; // vs Ethereum L1; null on the L1 row or when L1 is unavailable
}

export interface L2LadderRow {
  id: string;
  name: string;
  kind: 'l1' | 'opstack' | 'arbitrum';
  gas_price_gwei: number;
  actions: L2ActionCost[];
  error?: string;
}

export interface L2Ladder {
  rows: L2LadderRow[];
  note: string;
}

export interface TransferProfile {
  energy: number;
  bandwidth: number;
  share_pct: number;
}

export interface GasCalibration {
  usdt_holder: TransferProfile;
  usdt_new: TransferProfile;
  usdc_transfer_gas: number;
  usdc_samples: number;
  measured_at: string;
  windows: string;
}

export interface GasLiveData {
  as_of: string;
  btc: BtcLive | null;
  btc_error?: string;
  tron: TronLive | null;
  tron_error?: string;
  l2: L2Ladder;
  calibration: GasCalibration | null;
  calibration_error?: string;
}

// --- Crypto Pulse: Payment Check (field names mirror backend/payment_check_*.go) ---

export type PaymentAsset = 'USDT' | 'USDC' | 'ETH' | 'TRX';
export type PaymentNetwork = 'tron' | 'eth' | 'arb' | 'op' | 'base';
export type PaymentTokenTier = 'native' | 'bridged' | 'counterfeit' | 'other';
export type PaymentFinalityLevel = 'DANGER' | 'SOFT' | 'SAFE' | 'FINALIZED';

export interface PaymentHeads {
  latest: number;
  latest_time: number;
  safe: number;
  safe_time: number;
  finalized: number;
  final_time: number;
  safe_lag_sec: number;
  final_lag_sec: number;
}

export interface PaymentBalanceRow {
  label: string;
  contract: string;
  tier: PaymentTokenTier;
  finalized: string;
  safe_delta: string;
  latest_delta: string;
  error?: string;
}

export interface PaymentTx {
  tx_hash: string;
  explorer_url: string;
  direction: 'in' | 'out';
  timestamp: string;
  amount: string;
  symbol: string;
  token_contract: string;
  token_tier: PaymentTokenTier;
  counterparty: string;
  block: number;
  failed: boolean;
  level: PaymentFinalityLevel;
  progress: number;
  est_sec_left: number;
  flags: string[];
  counterparty_hits?: string[];
}

export interface PaymentAlert {
  severity: 'critical' | 'warning';
  code:
    | 'own_address_frozen'
    | 'own_address_sanctioned'
    | 'counterfeit_received'
    | 'poisoning_received'
    | 'sent_to_lookalike';
  message: string;
  tx_hash?: string;
}

export interface PaymentHistoryScope {
  hosts: string[];
  txlist: number;
  tokentx: number;
  txlistinternal: number;
  trc20: number;
  truncated: boolean;
}

export interface PaymentLiveResponse {
  asset: PaymentAsset;
  network: PaymentNetwork;
  network_label: string;
  address: string;
  as_of: string;
  heads: PaymentHeads;
  balance: PaymentBalanceRow[];
  balance_error?: string;
  latest: PaymentTx | null;
  alerts: PaymentAlert[];
  sources: AddressRiskSource[];
}

export interface PaymentHistoryResponse {
  asset: PaymentAsset;
  network: PaymentNetwork;
  address: string;
  as_of: string;
  since: string;
  txs: PaymentTx[];
  scope: PaymentHistoryScope;
  sources: AddressRiskSource[];
}

export interface PaymentQueryParams {
  asset: PaymentAsset;
  network: PaymentNetwork;
  address: string;
}


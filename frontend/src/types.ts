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
  storage_tier: string; // ACTIVE | LONG_TERM
}

export interface StorageDashboardData {
  billing: StorageStats | null;
  breakdown: StorageBreakdown | null;
  top_tables: TopTable[] | null;
  search_indexes: SearchIndexInfo[] | null;
  dataset_storage: DatasetStorage[] | null;
  cold_tables: ColdTable[] | null;
}

export interface SlotStatePoint {
  period_start: string;
  state: string; // PENDING | RUNNING
  slots: number;
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
  slot_timeline: SlotStatePoint[] | null;
  top_jobs: TopSlotJob[] | null;
  slot_usage: SlotUsage[] | null;
  queue_stats: QueueStats | null;
  reservations: ReservationPoint[] | null;
}

export interface CostSummary {
  bytes_billed: number;
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

export type GranteeKind = 'user' | 'serviceAccount' | 'group' | 'domain' | 'special' | 'public';

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
  sensitive_columns: SensitiveColumn[] | null;
  datasets_scanned: number;
  datasets_total: number;
}

export interface NewActor {
  email: string;
  first_seen: string;
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
}

export interface WhaleAddress {
  address: string;
  total: number;
  tx_count: number;
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

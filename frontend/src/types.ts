export interface QueryFilters {
  region: string;
  dataset: string;
  table: string;
  user_email: string;
  time_range: string;
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

export interface StorageDashboardData {
  billing: StorageStats | null;
  breakdown: StorageBreakdown | null;
  top_tables: TopTable[] | null;
  search_indexes: SearchIndexInfo[] | null;
}

export interface SlotTimepoint {
  period_start: string;
  concurrent_slots: number;
}

export interface TopSlotJob {
  job_id: string;
  user_email: string;
  total_slot_ms: number;
}

export interface SlotUsage {
  usage_hour: string;
  avg_slots: number;
}

export interface ComputeDashboardData {
  slot_timeline: SlotTimepoint[] | null;
  top_jobs: TopSlotJob[] | null;
  slot_usage: SlotUsage[] | null;
}

export interface CostSummary {
  bytes_billed: number;
}

export interface UserSpend {
  user_email: string;
  total_bytes: number;
}

export interface CostDashboardData {
  summary: CostSummary | null;
  spend_by_user: UserSpend[] | null;
}

export interface Recommendation {
  recommender: string;
  description: string;
  category: string;
  projected_savings_usd: number;
}

export interface InsightsDashboardData {
  recommendations: Recommendation[] | null;
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

export interface IAMDashboardData {
  summary: IAMSummary | null;
  timeline: UsageTimepoint[] | null;
  top_callers: TopCaller[] | null;
  inactive_7d: InactiveEmail[] | null;
  inactive_30d: InactiveEmail[] | null;
  inactive_90d: InactiveEmail[] | null;
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

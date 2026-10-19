export interface BtcSourceStatus {
  id: string;
  label: string;
  status: 'ok' | 'stale' | 'error' | 'disabled' | 'geo_blocked' | 'rate_limited' | string;
  via?: string;
  as_of?: string;
  error?: string;
  license?: string;
}

export interface BtcBQStatus {
  job: string;
  jobs: boolean;
  state: 'off' | 'cold_start' | 'ready' | 'retrying' | 'stopped' | 'budget_paused' | string;
  synced_through: string;
  coverage_from: string;
  last_error?: string;
  failures?: number;
  stop_reason?: string;
  next_run_after?: string;
}

export interface BtcBaselineStatus {
  available: boolean;
  generation: number;
  as_of: string;
  built_at: string;
  building?: boolean;
  pending: boolean;
  baseline_sats: number;
  baseline_btc: number;
  expected_sats: number;
  delta_pct?: number | null;
  suspect: boolean;
  cm_sply_cur_btc?: number | null;
  allow_ui: boolean;
  min_interval_days: number;
  last_baseline_at?: string;
  next_allowed_at?: string;
  cli_command: string;
}

export interface BtcBaselinePlan {
  as_of: string;
  estimated_bytes: number;
  estimated_gib: number;
  estimated_usd: number;
  token?: string;
  expires_at?: string;
}

export interface BtcDateRange {
  from: string;
  to: string;
}

export interface BtcPriceCoverage {
  complete: boolean;
  earliest: string;
  latest: string;
  total_days: number;
  missing_days: number;
  missing_ranges?: BtcDateRange[];
  sources: Record<string, number>;
}

export interface BtcMetaProvider {
  id: string;
  label: string;
  enabled: boolean;
  license?: string;
}

export interface BtcMetaCosts {
  first_day_usd: number;
  steady_month_usd: number;
  utxo_baseline_usd: number;
  rollup_1d_gib: number;
  coin_age_run_gib: number;
  utxo_baseline_gib: number;
}

export interface BtcMetaBigQuery {
  jobs: boolean;
  data_available: boolean;
  project: string;
  cadence_days: number;
  budget_gib: number;
  month_billed_gib: number;
  reserved_gib: number;
  budget_warning?: string;
  baseline_ui: boolean;
  stopped_jobs?: string[];
  statuses: Record<string, BtcBQStatus>;
  costs: BtcMetaCosts;
}

export interface BtcMetaData {
  providers: BtcMetaProvider[];
  bigquery: BtcMetaBigQuery;
  price_coverage: BtcPriceCoverage;
}

export interface BtcDifficultyAdj {
  progress_pct: number;
  est_change_pct: number;
  prev_change_pct: number;
  remaining_blocks: number;
  est_retarget_at: string;
  next_retarget_height: number;
  avg_block_seconds: number;
}

export interface BtcHalving {
  next_height: number | null;
  blocks_remaining: number | null;
  est_at: string | null;
  subsidy_now_btc: number | null;
  subsidy_next_btc: number | null;
  scheduled_supply_btc: number | null;
  pct_of_21m: number | null;
}

export interface BtcLiveData {
  as_of: string;
  price_usd: number | null;
  change_24h_pct: number | null;
  tip_height: number | null;
  tip_time: string;
  next_block_sat_vb: number;
  next_block_usd: number | null;
  mempool_vmb: number;
  blocks_to_clear: number;
  difficulty: BtcDifficultyAdj | null;
  halving: BtcHalving;
  hashrate_7d_ehs: number | null;
  sources: BtcSourceStatus[];
}

export interface BtcTxTemplateCost {
  id: string;
  label: string;
  vbytes: number;
  next_block_sats: number;
  next_block_usd: number | null;
  half_hour_sats: number;
  half_hour_usd: number | null;
  economy_sats: number;
  economy_usd: number | null;
}

export interface BtcConsolidationSignal {
  open: boolean;
  economy_sat_vb: number;
  threshold_sat_vb: number;
  blocks_to_clear: number;
  max_blocks_to_clear: number;
  reason: string;
}

export interface BtcMempoolStatPoint {
  ts: string;
  total_vmb: number;
  band_0_1_vmb: number;
  band_1_2_vmb: number;
  band_2_5_vmb: number;
  band_5_10_vmb: number;
  band_10_50_vmb: number;
  band_50_plus_vmb: number;
}

export interface BtcBlockFeeRatePoint {
  ts: string;
  avg_height: number;
  p10_sat_vb: number;
  p25_sat_vb: number;
  p50_sat_vb: number;
  p75_sat_vb: number;
  p90_sat_vb: number;
}

export interface BtcFeesData {
  as_of: string;
  period: string;
  price_usd: number | null;
  templates: BtcTxTemplateCost[];
  consolidation: BtcConsolidationSignal;
  mempool_history: BtcMempoolStatPoint[];
  block_fee_rates: BtcBlockFeeRatePoint[];
  sources: BtcSourceStatus[];
}

export interface BtcOnchainDay {
  date: string;
  blocks: number;
  empty_blocks: number;
  scheduled_issuance_sats: number;
  claimed_issuance_sats: number;
  avg_difficulty: number;
  implied_hashrate_ehs: number;
  txs: number;
  coinbase_txs: number;
  vbytes: number;
  bytes: number;
  witness_bytes: number;
  witness_share_pct: number;
  fees_sats: number;
  coinbase_sats: number;
  created_sats: number;
  opret_value_sats: number;
  fee_share_pct: number;
  fee_p10_sat_vb: number;
  fee_p25_sat_vb: number;
  fee_p50_sat_vb: number;
  fee_p75_sat_vb: number;
  fee_p90_sat_vb: number;
  vb_payment: number;
  vb_consolidation: number;
  vb_batch: number;
  vb_inscription_like: number;
  vb_runes: number;
  vb_op_return: number;
  vb_coinbase: number;
  opret_outputs: number;
  opret_bytes: number;
  runestones: number;
  opret_large: number;
  out_p2tr: number;
  out_p2wpkh: number;
  out_p2wsh: number;
  out_p2sh: number;
  out_p2pkh: number;
  out_anchor: number;
  out_all: number;
  in_p2tr: number;
  in_p2wpkh: number;
  in_p2wsh: number;
  in_p2sh: number;
  in_p2pkh: number;
  in_anchor: number;
  in_all: number;
  band_lt_0_001: number;
  band_0_001_0_01: number;
  band_0_01_0_1: number;
  band_0_1_1: number;
  band_1_10: number;
  band_10_100: number;
  band_ge_100: number;
  active_addresses: number;
}

export interface BtcOnchainData {
  days: number;
  rows: BtcOnchainDay[];
  status: BtcBQStatus;
  sources: BtcSourceStatus[];
}

export interface BtcExchangeFlowPoint {
  date: string;
  inflow_usd: number | null;
  outflow_usd: number | null;
  net_flow_usd: number | null;
  supply_ex_btc: number | null;
  supply_cur_btc: number | null;
  exchange_share_pct: number | null;
  provisional?: boolean;
}

export interface BtcTreasuryCompany {
  name: string;
  symbol: string;
  country: string;
  total_holdings_btc: number;
  total_entry_value_usd: number;
  total_current_value_usd: number;
  pct_of_total_supply: number;
}

export interface BtcTreasuriesSummary {
  total_holdings_btc: number;
  total_value_usd: number;
  market_cap_dominance_pct: number;
  companies: BtcTreasuryCompany[];
}

export interface BtcHoldersData {
  as_of: string;
  days: number;
  flows: BtcExchangeFlowPoint[];
  treasuries: BtcTreasuriesSummary | null;
  sources: BtcSourceStatus[];
}

export interface BtcCoinageDay {
  date: string;
  spent_sats: number;
  cdd_sat_days: number;
  cdd_btc_days: number;
  dormancy_days: number;
  revived_1y_sats: number;
  revived_2y_sats: number;
  revived_5y_sats: number;
  spent_age_shares_pct: Record<string, number>;
  sopr: number | null;
  sopr_adjusted: number | null;
  sopr_sth: number | null;
  sopr_lth: number | null;
  blocked?: string;
}

export interface BtcHolderDay {
  date: string;
  baseline_generation: number;
  supply_sats: number;
  supply_btc: number;
  lth_supply_sats: number;
  sth_supply_sats: number;
  hodl_waves_pct: Record<string, number>;
  realized_cap_usd: number | null;
  realized_price_usd: number | null;
  lth_realized_price_usd: number | null;
  sth_realized_price_usd: number | null;
  mvrv: number | null;
  supply_in_profit_pct: number | null;
  blocked?: string;
  warnings?: string[];
}

export interface BtcWhaleRevival {
  spend_tx: string;
  create_day: string;
  spend_day: string;
  spent_at: string;
  age_days: number;
  sats: number;
  btc: number;
  inputs: number;
}

export interface BtcHolderEngineStatus {
  state: 'ok' | 'halted' | string;
  halted_day?: string;
  reason?: string;
  since?: string;
  remedy?: string;
}

export interface BtcOnchainHoldersData {
  days: number;
  coinage: BtcCoinageDay[];
  holders: BtcHolderDay[];
  whales: BtcWhaleRevival[];
  coinage_status: BtcBQStatus;
  onchain_status: BtcBQStatus;
  baseline: BtcBaselineStatus;
  holder_engine: BtcHolderEngineStatus;
  price_coverage: BtcPriceCoverage;
  sources: BtcSourceStatus[];
}

export interface BtcVenueRow {
  venue: string;
  kind: 'cex_offshore' | 'cex_us' | 'dex' | 'regulated_futures' | string;
  spot_24h_usd: number | null;
  depth_2pct_usd: number | null;
  spread_pct: number | null;
  deriv_24h_usd: number | null;
  oi_usd: number | null;
  oi_btc: number | null;
  funding_8h_pct: number | null;
  funding_apr_pct: number | null;
  premium_pct: number | null;
  weekly?: boolean;
  sources: BtcSourceStatus[];
}

export interface BtcVenueRatio {
  venue: string;
  spot_24h_usd: number;
  deriv_24h_usd: number;
  ratio: number | null;
}

export interface BtcMarketsRatios {
  curated_spot_24h_usd: number | null;
  curated_deriv_24h_usd: number | null;
  major_deriv_spot: number | null;
  all_reported_spot_24h_usd: number | null;
  all_reported_deriv_24h_usd: number | null;
  all_reported_deriv_spot: number | null;
  all_reported_unverified: boolean;
  per_venue: BtcVenueRatio[] | null;
  note?: string;
}

export interface BtcOIShare {
  venue: string;
  kind: string;
  oi_usd: number;
  oi_btc: number;
  share_pct: number;
  weekly?: boolean;
  via?: string;
}

export interface BtcBasisPoint {
  venue: string;
  instrument: string;
  expiry: string;
  days_to_expiry: number;
  future_price: number;
  spot_index: number;
  basis_apr_pct: number;
  oi_usd: number;
  volume_24h_usd: number;
}

export interface BtcDVOLPoint {
  date: string;
  open: number;
  high: number;
  low: number;
  close: number;
}

export interface BtcOKXRubikPoint {
  date: string;
  oi_usd: number;
  volume_24h_usd: number;
}

export interface BtcCFTCWeekPoint {
  report_date: string;
  market_name: string;
  contract_code: string;
  contract_size_btc: number;
  open_interest_contracts: number;
  open_interest_btc: number;
  asset_managers_net_btc: number;
  leveraged_funds_net_btc: number;
  dealers_net_btc: number;
  other_reportables_net_btc: number;
}

export interface BtcCFTCSummary {
  as_of_report_date: string;
  cme_oi_btc: number;
  cme_micro_oi_btc: number;
  cde_oi_btc: number;
  total_cme_reg_btc: number;
  cme_history: BtcCFTCWeekPoint[] | null;
  latest_by_market: BtcCFTCWeekPoint[] | null;
}

export interface BtcMarketDailyPoint {
  date: string;
  major_deriv_spot: number | null;
  all_reported_deriv_spot: number | null;
  total_oi_usd: number | null;
  oi_to_mcap_pct: number | null;
  dex_share_pct: number | null;
  us_regulated_share_pct: number | null;
  funding_oi_weighted_apr: number | null;
  dvol: number | null;
  rv30: number | null;
  vrp: number | null;
}

export interface BtcMarketsData {
  as_of: string;
  price_usd: number | null;
  market_cap_usd: number | null;
  total_oi_usd: number | null;
  venues: BtcVenueRow[];
  ratios: BtcMarketsRatios;
  oi_shares: BtcOIShare[] | null;
  oi_to_mcap_pct: number | null;
  dex_share_pct: number | null;
  us_regulated_share_pct: number | null;
  funding_oi_weighted_apr: number | null;
  basis: BtcBasisPoint[] | null;
  dvol: BtcDVOLPoint[] | null;
  dvol_latest: number | null;
  rv30: number | null;
  vrp: number | null;
  cot: BtcCFTCSummary | null;
  history: BtcMarketDailyPoint[] | null;
  okx_rubik: BtcOKXRubikPoint[] | null;
  caveats: string[];
  sources: BtcSourceStatus[];
}

export type BtcValuationRange = '1y' | '4y' | 'all';

export interface BtcFnGPoint {
  date: string;
  value: number;
  classification: string;
}

export interface BtcHalvingCyclePoint {
  day: number;
  date: string;
  indexed: number;
  price: number;
}

export interface BtcHalvingCycleSeries {
  epoch: number;
  halving_date: string;
  label: string;
  base_price: number;
  points: BtcHalvingCyclePoint[];
}

export interface BtcValuationPoint {
  date: string;
  close_usd: number;
  sma200?: number | null;
  wma200?: number | null;
  wma200_monthly_pct?: number | null;
  mayer?: number | null;
  drawdown_pct: number;
  cm_mvrv?: number | null;
  biglens_mvrv?: number | null;
  mvrv_delta_pct?: number | null;
  puell_cm?: number | null;
  puell_chain?: number | null;
  realized_price_usd?: number | null;
  sth_realized_price_usd?: number | null;
  lth_realized_price_usd?: number | null;
}

export interface BtcValuationLatest {
  date: string;
  close_usd: number | null;
  ath_usd: number | null;
  ath_date?: string;
  drawdown_pct: number | null;
  sma200: number | null;
  wma200: number | null;
  wma200_monthly_pct: number | null;
  mayer: number | null;
  mayer_pctl_since_2011: number | null;
  cm_mvrv: number | null;
  cm_mvrv_pctl_since_2011: number | null;
  biglens_mvrv?: number | null;
  mvrv_delta_pct?: number | null;
  puell_cm: number | null;
  puell_chain?: number | null;
  realized_price_usd?: number | null;
  sth_realized_price_usd?: number | null;
  lth_realized_price_usd?: number | null;
  realized_price_blocked?: string;
  halving_cycle_day?: number | null;
  fear_greed_value?: number | null;
  fear_greed_class?: string;
}

export interface BtcValuationData {
  as_of: string;
  range: BtcValuationRange;
  warming_up: boolean;
  price_coverage: BtcPriceCoverage;
  latest: BtcValuationLatest;
  series: BtcValuationPoint[];
  cycles: BtcHalvingCycleSeries[];
  fear_greed: BtcFnGPoint[];
  sources: BtcSourceStatus[];
}

export type BtcMiningPeriod = '3m' | '1y' | '3y';

export interface BtcConcentrationMetric {
  value: number;
  denominator: number;
  unit: string;
  coverage_pct: number;
  method: string;
}

export interface BtcPoolConcentration {
  period: string;
  total_blocks: number;
  known_blocks: number;
  nakamoto: BtcConcentrationMetric;
  hhi: BtcConcentrationMetric;
}

export interface BtcMiningPoolRow {
  rank: number;
  name: string;
  slug: string;
  block_count: number;
  share_pct: number;
  empty_blocks: number;
  avg_match_rate: number;
}

export interface BtcHashratePoint {
  date: string;
  ts: string;
  hashrate_ehs: number;
  implied_hashrate_ehs?: number | null;
  delta_pct?: number | null;
  sma30_ehs?: number | null;
  sma60_ehs?: number | null;
}

export interface BtcDifficultyHistoryPoint {
  date: string;
  ts: string;
  height: number;
  difficulty: number;
  adjustment_pct: number;
}

export interface BtcRibbonState {
  state: 'capitulation' | 'recovery' | 'normal' | 'warming_up' | string;
  sma30?: (number | null)[];
  sma60?: (number | null)[];
  last_cross_up_days?: number | null;
}

export interface BtcHashpricePoint {
  date: string;
  btc_per_ph_day: number;
  usd_per_ph_day: number | null;
}

export interface BtcRewardStats144 {
  start_block: number;
  end_block: number;
  total_reward_sats: number;
  total_reward_btc: number;
  total_fee_sats: number;
  total_fee_btc: number;
  subsidy_sats: number;
  subsidy_btc: number;
  fee_share_pct: number;
  total_tx: number;
}

export interface BtcSecurityBudget {
  daily_miner_revenue_btc: number | null;
  daily_miner_revenue_usd: number | null;
  annualized_revenue_usd: number | null;
  market_cap_usd: number | null;
  security_budget_pct: number | null;
}

export interface BtcMiningChainDay {
  day: string;
  blocks: number;
  empty_blocks: number;
  avg_difficulty: number;
  implied_hashrate_ehs: number;
  mempool_hashrate_ehs?: number | null;
  hashrate_delta_pct?: number | null;
  scheduled_sats: number;
  claimed_sats: number;
  fees_sats: number;
  fee_share_pct: number;
}

export interface BtcNodeNetworkMix {
  ipv4: number;
  ipv6: number;
  tor: number;
  i2p: number;
  cjdns: number;
}

export interface BtcNodeBucket {
  label: string;
  count: number;
  share_pct: number;
}

export interface BtcNodeCountry {
  code: string;
  name: string;
  count: number;
  share_pct: number;
}

export interface BtcNodeASN {
  asn: number;
  org: string;
  count: number;
  share_pct: number;
  is_cloud: boolean;
}

export interface BtcNodes {
  as_of: string;
  reachable: number;
  clearnet: number;
  network: BtcNodeNetworkMix;
  clients: BtcNodeBucket[];
  core_versions: BtcNodeBucket[];
  countries: BtcNodeCountry[];
  total_asns: number;
  asns: BtcNodeASN[];
  cloud_share: BtcConcentrationMetric;
  asn_nakamoto: BtcConcentrationMetric;
  country_nakamoto: BtcConcentrationMetric;
  asn_hhi: BtcConcentrationMetric;
  unresolved: number;
  attribution: string;
}

export interface BtcMiningData {
  as_of: string;
  period: BtcMiningPeriod | string;
  price_usd: number | null;
  current_hashrate_ehs: number | null;
  current_difficulty: number | null;
  hashrate: BtcHashratePoint[];
  difficulty: BtcDifficultyAdj | null;
  adjustments: BtcDifficultyHistoryPoint[];
  ribbon: BtcRibbonState;
  hashprice: BtcHashpricePoint[];
  hashprice_now: BtcHashpricePoint | null;
  pools: BtcMiningPoolRow[];
  concentration: BtcPoolConcentration;
  rewards_144: BtcRewardStats144 | null;
  fee_share_pct: number | null;
  security_budget: BtcSecurityBudget;
  halving: BtcHalving;
  nodes: BtcNodes | null;
  chain?: BtcMiningChainDay[];
  sources: BtcSourceStatus[];
}

export interface BtcOverviewConcentration {
  pool_period: string;
  pool_nakamoto: BtcConcentrationMetric;
  pool_hhi: BtcConcentrationMetric;
  node_reachable: number;
  node_clearnet: number;
  node_clearnet_share_pct: number | null;
  node_country_nakamoto?: BtcConcentrationMetric;
  node_asn_nakamoto?: BtcConcentrationMetric;
  node_asn_hhi?: BtcConcentrationMetric;
  node_cloud_share?: BtcConcentrationMetric;
}

export interface BtcOverviewCards {
  close_usd: number | null;
  market_cap_usd: number | null;
  dominance_pct: number | null;
  drawdown_pct: number | null;
  ath_usd: number | null;
  ath_date?: string;
  rv30: number | null;
  dvol: number | null;
  vrp: number | null;
  funding_oi_weighted_apr: number | null;
  total_oi_usd: number | null;
  major_deriv_spot: number | null;
  all_reported_deriv_spot: number | null;
  all_reported_unverified: boolean;
  mayer: number | null;
  mayer_pctl_since_2011: number | null;
  cm_mvrv: number | null;
  cm_mvrv_pctl_since_2011: number | null;
  biglens_mvrv?: number | null;
  mvrv_delta_pct?: number | null;
  realized_price_blocked?: string;
  hashprice_usd_per_ph_day: number | null;
  hashprice_btc_per_ph_day: number | null;
  exchange_net_flow_7d_usd: number | null;
  exchange_net_flow_7d_btc: number | null;
  exchange_flow_flash?: boolean;
  concentration: BtcOverviewConcentration;
}

export interface BtcRegimeSignal {
  id: string;
  label: string;
  state: string;
  summary: string;
  rule: string;
}

export interface BtcOverviewSupply {
  tip_height: number | null;
  scheduled_supply_btc: number | null;
  max_supply_btc: number;
  mined_pct: number | null;
  subsidy_btc: number | null;
  annual_issuance_btc: number | null;
  annual_inflation_pct: number | null;
  exchange_balance_btc: number | null;
  exchange_balance_pct: number | null;
  treasuries_btc: number | null;
  treasuries_pct: number | null;
  treasuries_companies: number;
}

export interface BtcOverviewSentiment {
  fear_greed_value?: number | null;
  fear_greed_class?: string;
  fear_greed_history: BtcFnGPoint[];
}

export interface BtcVerificationRow {
  id: string;
  label: string;
  date?: string;
  unit: string;
  api_label: string;
  api_value: number | null;
  chain_label: string;
  chain_value: number | null;
  delta_pct?: number | null;
  status: string;
  note?: string;
}

export interface BtcOverviewData {
  as_of: string;
  cards: BtcOverviewCards;
  price_chart: BtcValuationPoint[];
  signals: BtcRegimeSignal[];
  supply: BtcOverviewSupply;
  sentiment: BtcOverviewSentiment;
  verification: BtcVerificationRow[];
  sources: BtcSourceStatus[];
}



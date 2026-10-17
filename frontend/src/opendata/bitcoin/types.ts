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
  next_height: number;
  blocks_remaining: number;
  est_at: string;
  subsidy_now_btc: number;
  subsidy_next_btc: number;
  scheduled_supply_btc: number;
  pct_of_21m: number;
}

export interface BtcLiveData {
  as_of: string;
  price_usd: number | null;
  change_24h_pct: number | null;
  tip_height: number;
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

export interface BtcOnchainHoldersData {
  days: number;
  coinage: BtcCoinageDay[];
  holders: BtcHolderDay[];
  whales: BtcWhaleRevival[];
  coinage_status: BtcBQStatus;
  onchain_status: BtcBQStatus;
  baseline: BtcBaselineStatus;
  price_coverage: BtcPriceCoverage;
  sources: BtcSourceStatus[];
}

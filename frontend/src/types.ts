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

export interface StorageDashboardData {
  billing: StorageStats | null;
  breakdown: StorageBreakdown | null;
  top_tables: TopTable[] | null;
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

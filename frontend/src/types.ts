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

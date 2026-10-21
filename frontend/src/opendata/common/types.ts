export interface SourceStatus {
  id: string;
  label: string;
  status: 'ok' | 'stale' | 'error' | 'disabled' | 'geo_blocked' | 'rate_limited' | string;
  via?: string;
  as_of?: string;
  error?: string;
  license?: string;
}

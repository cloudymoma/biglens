const SATS_PER_BTC = 100_000_000;

export const satsToBTC = (sats: number): number => sats / SATS_PER_BTC;

export const fmtBTC = (btc: number | null | undefined, decimals = 2): string => {
  if (btc === null || btc === undefined || Number.isNaN(btc)) return '—';
  if (Math.abs(btc) >= 1_000_000) return `${(btc / 1_000_000).toFixed(2)}M BTC`;
  if (Math.abs(btc) >= 1_000) return `${btc.toLocaleString(undefined, { maximumFractionDigits: 0 })} BTC`;
  return `${btc.toLocaleString(undefined, { minimumFractionDigits: decimals, maximumFractionDigits: decimals })} BTC`;
};

export const fmtBTCFromSats = (sats: number | null | undefined, decimals = 2): string => {
  if (sats === null || sats === undefined || Number.isNaN(sats)) return '—';
  return fmtBTC(sats / SATS_PER_BTC, decimals);
};

export const fmtSats = (sats: number | null | undefined): string => {
  if (sats === null || sats === undefined || Number.isNaN(sats)) return '—';
  return `${Math.round(sats).toLocaleString()} sats`;
};

export const fmtSatVB = (rate: number | null | undefined): string => {
  if (rate === null || rate === undefined || Number.isNaN(rate)) return '—';
  if (rate < 10) return `${rate.toFixed(2)} sat/vB`;
  return `${rate.toFixed(1)} sat/vB`;
};

// BigQuery jobs panel formatters: times are UTC; missing values render "—".
// No imports: the Node test runner loads this file directly.

const parseISO = (iso: string | null | undefined): Date | null => {
  if (!iso) return null;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? null : new Date(t);
};

const pad2 = (n: number): string => String(n).padStart(2, '0');

const fmtDateUTC = (d: Date): string => `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())}`;

// fmtBytesGiB formats a byte count in GiB ("1.00 GiB"), or "—" when missing.
export const fmtBytesGiB = (bytes: number | null | undefined, decimals = 2): string => {
  if (bytes === null || bytes === undefined || Number.isNaN(bytes)) return '—';
  return `${(bytes / 1024 ** 3).toFixed(decimals)} GiB`;
};

// fmtDateTimeUTC: "2026-10-19 14:00 UTC".
export const fmtDateTimeUTC = (iso: string | null | undefined): string => {
  const d = parseISO(iso);
  return d ? `${fmtDateUTC(d)} ${pad2(d.getUTCHours())}:${pad2(d.getUTCMinutes())} UTC` : '—';
};

// fmtRelative: "now", "in 39m", "in 2h 5m", "in 6d 17h", "3h ago".
const fmtRelative = (diffMin: number): string => {
  if (diffMin === 0) return 'now';
  const abs = Math.abs(diffMin);
  const days = Math.floor(abs / 1440);
  const hours = Math.floor((abs % 1440) / 60);
  const mins = abs % 60;
  let body: string;
  if (days > 0) body = hours > 0 ? `${days}d ${hours}h` : `${days}d`;
  else if (hours > 0) body = mins > 0 ? `${hours}h ${mins}m` : `${hours}h`;
  else body = `${mins}m`;
  return diffMin > 0 ? `in ${body}` : `${body} ago`;
};

// fmtNextCheckUTC formats next_check_at in UTC with its relative time
// ("2026-10-19 14:00 UTC (in 39m)"), or "—" when the schedule is off.
export const fmtNextCheckUTC = (nextCheckIso: string | null | undefined, nowIso?: string | null): string => {
  const target = parseISO(nextCheckIso);
  if (!target) return '—';
  const dt = fmtDateTimeUTC(nextCheckIso);
  const now = nowIso ? parseISO(nowIso) : new Date();
  if (!now) return dt;
  return `${dt} (${fmtRelative(Math.round((target.getTime() - now.getTime()) / 60_000))})`;
};

// fmtLastRunUTC formats a job's last run (UTC time, trigger, result, GiB
// billed), or "—" when the job has not run yet.
export const fmtLastRunUTC = (
  status:
    | {
        last_run_at?: string | null;
        last_run_trigger?: string | null;
        last_run_result?: string | null;
        last_run_bytes_billed?: number | null;
      }
    | null
    | undefined,
): string => {
  if (!status || !status.last_run_at) return '—';
  const dt = fmtDateTimeUTC(status.last_run_at);
  if (dt === '—') return '—';
  return `${dt} · ${status.last_run_trigger || '—'} · ${status.last_run_result || '—'} · ${fmtBytesGiB(status.last_run_bytes_billed)}`;
};

// settledThroughUTC is the newest UTC day a manual run can fetch: the day
// before the one that settles at next_new_day_at (≈ 06:30 UTC, after the 6 h
// block-timestamp slack). "2026-10-20T06:30:00Z" → "2026-10-18".
export const settledThroughUTC = (nextNewDayIso: string | null | undefined): string | null => {
  const d = parseISO(nextNewDayIso);
  if (!d) return null;
  return fmtDateUTC(new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate() - 2)));
};

// mapBitcoinBQError maps HTTP errors from /bigquery/estimate and
// /bigquery/run to plain operator messages.
export const mapBitcoinBQError = (status: number | undefined, serverMessage?: string): string => {
  const msg = (serverMessage ?? '').trim();
  if (status === 403) {
    return 'BigQuery actions are restricted to this computer (loopback 127.0.0.1 / ::1).';
  }
  if (status === 429) {
    return msg
      ? `Rate limited (at most one manual run every 5 minutes): ${msg}`
      : 'Rate limited: at most one manual run per job every 5 minutes.';
  }
  if (status === 409) {
    const lower = msg.toLowerCase();
    if (lower.includes('estimate changed')) {
      return 'The dry-run estimate changed by more than 10%. Click "Run now" again to review the updated estimate.';
    }
    if (lower.includes('already running')) {
      return 'This job is already running. Wait for the current run to finish.';
    }
    if (lower.includes('over_budget')) {
      return 'Refused: the run’s budget reservation exceeds the remaining monthly BigQuery budget (or the per-run cap).';
    }
    if (lower.includes('nothing to do')) {
      return 'Already up to date through the latest settled UTC day (days settle ≈ 06:30 UTC).';
    }
    return msg || 'Conflict: this BigQuery job cannot be started right now.';
  }
  if (status === 503) {
    return 'BigQuery runner unavailable: the server has no SQLite store or BigQuery client.';
  }
  return msg || 'BigQuery request failed.';
};

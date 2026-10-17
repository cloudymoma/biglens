import { AlertTriangle, CheckCircle2, Clock, PauseCircle, Terminal, XCircle } from 'lucide-react';
import type { BtcBQStatus, BtcMetaBigQuery } from './types';

export default function BigQueryStatusChip({
  bigquery,
  job = 'btc_onchain',
}: {
  bigquery?: BtcMetaBigQuery | null;
  job?: string;
}) {
  if (!bigquery) return null;

  const status: BtcBQStatus | undefined =
    bigquery.statuses?.[job] ?? bigquery.statuses?.rollup;
  const state = status?.state ?? (bigquery.jobs ? 'cold_start' : 'off');
  const billed = bigquery.month_billed_gib ?? 0;
  const budget = bigquery.budget_gib ?? 0;

  const stateMeta: Record<
    string,
    { label: string; cls: string; icon: typeof CheckCircle2 }
  > = {
    ready: {
      label: 'BQ Ready',
      cls: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300',
      icon: CheckCircle2,
    },
    cold_start: {
      label: 'BQ Backfilling',
      cls: 'border-sky-500/30 bg-sky-500/10 text-sky-300',
      icon: Clock,
    },
    retrying: {
      label: 'BQ Retrying',
      cls: 'border-amber-500/30 bg-amber-500/10 text-amber-300',
      icon: AlertTriangle,
    },
    budget_paused: {
      label: 'BQ Budget Paused',
      cls: 'border-amber-500/30 bg-amber-500/10 text-amber-300',
      icon: PauseCircle,
    },
    stopped: {
      label: 'BQ Stopped',
      cls: 'border-red-500/30 bg-red-500/10 text-red-300',
      icon: XCircle,
    },
    off: {
      label: 'BQ Jobs Off',
      cls: 'border-zinc-700 bg-zinc-800/60 text-zinc-400',
      icon: PauseCircle,
    },
  };

  const meta = stateMeta[state] ?? stateMeta.off;
  const Icon = meta.icon;
  const stoppedJob = status?.job || job;

  return (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <span
        title={status?.stop_reason || status?.last_error || `Job ${stoppedJob}: ${state}`}
        className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 font-medium ${meta.cls}`}
      >
        <Icon className="h-3.5 w-3.5" />
        <span>{meta.label}</span>
        {status?.synced_through && (
          <span className="opacity-80">· thru {status.synced_through}</span>
        )}
      </span>
      {state === 'stopped' && (
        <span
          title={status?.stop_reason || 'Repeated billed failures stopped this job'}
          className="inline-flex items-center gap-1 rounded-full border border-red-500/30 bg-red-950/40 px-2.5 py-0.5 font-mono text-[11px] text-red-200"
        >
          <Terminal className="h-3 w-3 text-red-400" />
          <span>./biglens-server --bitcoin-bq-resume {stoppedJob}</span>
        </span>
      )}
      {budget > 0 && (
        <span className="rounded-full border border-zinc-800 bg-zinc-900/70 px-2.5 py-0.5 text-[11px] text-zinc-400">
          Budget: {billed.toFixed(1)} / {budget.toFixed(0)} GiB
          {bigquery.reserved_gib > 0 ? ` (+${bigquery.reserved_gib.toFixed(1)} GiB reserved)` : ''}
        </span>
      )}
      {bigquery.budget_warning && (
        <span className="inline-flex items-center gap-1 rounded-full border border-amber-500/30 bg-amber-500/10 px-2.5 py-0.5 text-[11px] text-amber-300">
          <AlertTriangle className="h-3 w-3" />
          <span>{bigquery.budget_warning}</span>
        </span>
      )}
    </div>
  );
}

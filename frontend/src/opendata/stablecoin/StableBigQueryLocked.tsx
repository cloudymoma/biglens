import { AlertTriangle, CheckCircle2, Clock, Database, Eye, PauseCircle, Terminal, XCircle } from 'lucide-react';
import { cx } from './styles';
import type { StableBQJobStatus, StableMetaBigQuery } from './types';
import type { StableBigQueryControl } from './useStableBigQuery';

export function StableBigQuerySwitch({ control }: { control: StableBigQueryControl }) {
  const { show, available, jobsEnabled, reason, toggle } = control;

  const title = !available
    ? reason
    : 'Browser display preference (off by default) — reads pre-synced SQLite tables and never triggers a BigQuery query.';

  return (
    <div
      title={title}
      className="inline-flex items-center gap-2 rounded-lg border border-zinc-200 bg-white px-2.5 py-1.5 text-xs dark:border-zinc-800 dark:bg-zinc-900/80"
    >
      <Database className={`h-3.5 w-3.5 ${show ? 'text-amber-600 dark:text-amber-400' : cx.muted}`} aria-hidden="true" />
      <span className={`font-medium ${cx.text}`}>Show BigQuery panels</span>
      <button
        type="button"
        role="switch"
        aria-checked={show}
        aria-label="Show BigQuery panels"
        disabled={!available}
        onClick={toggle}
        className={`relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors ${cx.focus} ${
          !available
            ? 'cursor-not-allowed bg-zinc-200 opacity-50 dark:bg-zinc-800'
            : show
              ? 'cursor-pointer bg-emerald-600 dark:bg-emerald-500'
              : 'cursor-pointer bg-zinc-300 dark:bg-zinc-700'
        }`}
      >
        <span
          className={`inline-block h-3.5 w-3.5 transform rounded-full bg-white transition-transform ${
            show ? 'translate-x-4' : 'translate-x-1'
          }`}
        />
      </button>
      <span
        className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${
          jobsEnabled
            ? 'bg-emerald-600/15 text-emerald-800 dark:bg-emerald-500/15 dark:text-emerald-300'
            : available
              ? 'bg-sky-600/15 text-sky-800 dark:bg-sky-500/15 dark:text-sky-300'
              : 'bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400'
        }`}
      >
        {jobsEnabled ? 'Jobs: ON' : available ? 'Cached SQLite' : 'Jobs: OFF'}
      </span>
    </div>
  );
}

export function StableBigQueryStatusChip({ bigquery }: { bigquery?: StableMetaBigQuery | null }) {
  if (!bigquery) return null;

  const allStatuses = Object.values(bigquery.statuses ?? {});
  const stoppedStatus = allStatuses.find(s => s.state === 'stopped');
  const retryingStatus = allStatuses.find(s => s.state === 'retrying' || s.state === 'budget_paused');
  const readyStatus = allStatuses.find(s => s.state === 'ready');
  const status: StableBQJobStatus | undefined =
    stoppedStatus ?? retryingStatus ?? readyStatus ?? bigquery.statuses?.stable_eth ?? allStatuses[0];
  const state = status?.state ?? (bigquery.jobs ? 'cold_start' : 'off');
  const stoppedJobs =
    bigquery.stopped_jobs && bigquery.stopped_jobs.length > 0
      ? bigquery.stopped_jobs
      : stoppedStatus?.job
        ? [stoppedStatus.job]
        : state === 'stopped' && status?.job
          ? [status.job]
          : [];
  const billed = bigquery.month_billed_gib ?? 0;
  const budget = bigquery.monthly_budget_gib ?? 0;
  const reserved = bigquery.reserved_gib ?? 0;

  const stateMeta: Record<string, { label: string; cls: string; icon: typeof CheckCircle2 }> = {
    ready: {
      label: 'BQ Ready',
      cls: 'border-emerald-600/30 bg-emerald-600/10 text-emerald-800 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300',
      icon: CheckCircle2,
    },
    cold_start: {
      label: 'BQ Backfilling',
      cls: 'border-sky-600/30 bg-sky-600/10 text-sky-800 dark:border-sky-500/30 dark:bg-sky-500/10 dark:text-sky-300',
      icon: Clock,
    },
    retrying: {
      label: 'BQ Retrying',
      cls: 'border-amber-600/30 bg-amber-600/10 text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300',
      icon: AlertTriangle,
    },
    budget_paused: {
      label: 'BQ Budget Paused',
      cls: 'border-amber-600/30 bg-amber-600/10 text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300',
      icon: PauseCircle,
    },
    stopped: {
      label: 'BQ Stopped',
      cls: 'border-red-600/30 bg-red-600/10 text-red-800 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300',
      icon: XCircle,
    },
    off: {
      label: 'BQ Jobs Off',
      cls: 'border-zinc-200 bg-zinc-100 text-zinc-600 dark:border-zinc-700 dark:bg-zinc-800/60 dark:text-zinc-400',
      icon: PauseCircle,
    },
  };

  const meta = stateMeta[state] ?? stateMeta.off;
  const Icon = meta.icon;

  return (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <span
        title={status?.stop_reason || status?.last_error || bigquery.aggregate_exposure_note || `BigQuery state: ${state}`}
        className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 font-medium ${meta.cls}`}
      >
        <Icon className="h-3.5 w-3.5" aria-hidden="true" />
        <span>{meta.label}</span>
        {status?.synced_through && <span className="opacity-80">· thru {status.synced_through}</span>}
      </span>
      {stoppedJobs.map(sj => (
        <span
          key={sj}
          title={status?.stop_reason || 'Repeated billed failures stopped this job'}
          className="inline-flex items-center gap-1 rounded-full border border-red-600/30 bg-red-50 px-2.5 py-0.5 font-mono text-[11px] text-red-800 dark:border-red-500/30 dark:bg-red-950/40 dark:text-red-200"
        >
          <Terminal className="h-3 w-3 text-red-600 dark:text-red-400" aria-hidden="true" />
          <span>./biglens-server --stablecoin-bq-resume {sj}</span>
        </span>
      ))}
      {bigquery.jobs && budget > 0 && (
        <span className={`rounded-full border px-2.5 py-0.5 text-[11px] ${cx.border} ${cx.muted}`}>
          Budget: {billed.toFixed(1)} / {budget.toFixed(0)} GiB
          {reserved > 0 ? ` (+${reserved.toFixed(1)} GiB reserved)` : ''}
        </span>
      )}
    </div>
  );
}

export default function StableBigQueryLocked({
  title,
  description,
  bigquery,
  onEnableDisplay,
}: {
  title: string;
  description: string;
  bigquery?: StableMetaBigQuery | null;
  onEnableDisplay?: () => void;
}) {
  const canDisplay = Boolean(bigquery?.data_available);

  return (
    <div className={`${cx.card} border-dashed`}>
      <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
        <div className="space-y-2">
          <div className="inline-flex items-center gap-2 rounded-full border border-amber-600/30 bg-amber-600/10 px-2.5 py-0.5 text-xs font-medium text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300">
            <Database className="h-3.5 w-3.5" aria-hidden="true" />
            <span>BigQuery On-Chain Layer (Off by Default)</span>
          </div>
          <h3 className={cx.title}>{title}</h3>
          <p className={`max-w-2xl text-xs leading-relaxed ${cx.muted}`}>{description}</p>
        </div>

        {canDisplay && onEnableDisplay ? (
          <button
            type="button"
            onClick={onEnableDisplay}
            className={`${cx.button} ${cx.focus} shrink-0 font-semibold`}
          >
            <Eye className="h-3.5 w-3.5" aria-hidden="true" />
            <span>Show BigQuery panels ($0 read)</span>
          </button>
        ) : null}
      </div>

      {!canDisplay && (
        <div className={`mt-4 grid gap-3 md:grid-cols-2 ${cx.inset}`}>
          <div className="space-y-1.5">
            <div className={`flex items-center gap-1.5 text-xs font-medium ${cx.strong}`}>
              <Terminal className="h-3.5 w-3.5 text-amber-600 dark:text-amber-400" aria-hidden="true" />
              <span>Operator opt-in required (conf.yaml)</span>
            </div>
            <p className={`text-[11px] leading-relaxed ${cx.muted}`}>
              BigQuery background jobs (<code>stable_eth</code> and <code>stable_tron</code>) are disabled by default so
              visitors can never bill the operator&apos;s project. Browser requests read local SQLite only.
              {bigquery?.aggregate_exposure_note ? ` ${bigquery.aggregate_exposure_note}` : ''}
            </p>
          </div>
          <pre className={`overflow-x-auto rounded-lg border p-2.5 font-mono text-[11px] ${cx.border} ${cx.text}`}>
            {`stablecoin:
  bigquery:
    jobs: true
    initial_days: 90
    monthly_budget_gib: 512
    tron: true`}
          </pre>
        </div>
      )}
    </div>
  );
}

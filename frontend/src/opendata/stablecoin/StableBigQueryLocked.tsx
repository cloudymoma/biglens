import { useEffect, useRef, useState } from 'react';
import axios from 'axios';
import {
  AlertTriangle,
  Check,
  CheckCircle2,
  ChevronDown,
  Clock,
  Copy,
  Database,
  Eye,
  Loader2,
  PauseCircle,
  Play,
  Terminal,
  X,
  XCircle,
} from 'lucide-react';
import { estimateStableBQJob, runStableBQJob } from './api';
import { fmtBytesGiB, fmtDateTime, fmtLastRunUTC, fmtNextCheckUTC, mapStableBQError } from './format';
import { cx } from './styles';
import type { StableBQEstimateResponse, StableBQJobStatus, StableMetaBigQuery } from './types';
import type { StableBigQueryControl } from './useStableBigQuery';

const CONF_SNIPPET = `stablecoin:
  bigquery:
    jobs: true`;

function yesterdayUTC(referenceIso?: string | null): string | null {
  const base = referenceIso ? Date.parse(referenceIso) : Date.now();
  if (Number.isNaN(base)) return null;
  const d = new Date(base);
  const y = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate() - 1));
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${y.getUTCFullYear()}-${pad(y.getUTCMonth() + 1)}-${pad(y.getUTCDate())}`;
}

export function StableBigQuerySwitch({
  control,
  onOpenJobs,
}: {
  control: StableBigQueryControl;
  onOpenJobs?: () => void;
}) {
  const { show, available, jobsEnabled, reason, toggle } = control;

  const title = !available
    ? reason
    : 'Browser display preference (off by default) — reads pre-synced SQLite tables and never triggers a BigQuery query.';

  return (
    <div className="flex flex-wrap items-center gap-2">
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
      {!available && (
        <div className={`inline-flex flex-wrap items-center gap-1.5 text-[11px] ${cx.muted}`}>
          <span>No on-chain data yet: run a job once, or turn on the schedule.</span>
          {onOpenJobs && (
            <button
              type="button"
              onClick={onOpenJobs}
              className={`${cx.link} ${cx.focus} rounded font-medium`}
            >
              Open jobs panel
            </button>
          )}
        </div>
      )}
    </div>
  );
}

export function StableBigQueryStatusChip({
  bigquery,
  open = false,
  onToggle,
}: {
  bigquery?: StableMetaBigQuery | null;
  open?: boolean;
  onToggle?: () => void;
}) {
  if (!bigquery) return null;

  const allStatuses = Object.values(bigquery.statuses ?? {});
  const runningStatus = allStatuses.find(s => s.running);
  const stoppedStatus = allStatuses.find(s => s.state === 'stopped');
  const retryingStatus = allStatuses.find(s => s.state === 'retrying' || s.state === 'budget_paused');
  const readyStatus = allStatuses.find(s => s.state === 'ready');
  const status: StableBQJobStatus | undefined =
    runningStatus ?? stoppedStatus ?? retryingStatus ?? readyStatus ?? bigquery.statuses?.stable_eth ?? allStatuses[0];
  const state = runningStatus ? 'running' : (status?.state ?? (bigquery.jobs ? 'cold_start' : 'off'));
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
    running: {
      label: 'BQ Running',
      cls: 'border-sky-600/30 bg-sky-600/10 text-sky-800 dark:border-sky-500/30 dark:bg-sky-500/10 dark:text-sky-300',
      icon: Loader2,
    },
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
      <button
        type="button"
        aria-expanded={open}
        aria-controls="stable-bq-jobs-panel"
        onClick={onToggle}
        title={status?.stop_reason || status?.last_error || bigquery.aggregate_exposure_note || `BigQuery state: ${state}`}
        className={`inline-flex cursor-pointer items-center gap-1.5 rounded-full border px-2.5 py-0.5 font-medium transition ${cx.focus} ${meta.cls}`}
      >
        <Icon
          className={`h-3.5 w-3.5 ${state === 'running' ? 'animate-spin motion-reduce:animate-none' : ''}`}
          aria-hidden="true"
        />
        <span>{meta.label}</span>
        {status?.synced_through && <span className="opacity-80">· thru {status.synced_through}</span>}
        <ChevronDown className={`h-3 w-3 transition-transform ${open ? 'rotate-180' : ''}`} aria-hidden="true" />
      </button>
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

const JOB_LABELS: Record<string, { title: string; subtitle: string }> = {
  stable_eth: {
    title: 'stable_eth',
    subtitle: 'Ethereum ERC-20 transfers, large transfers (≥1M) & governance logs (USDT, USDC, EURC)',
  },
  stable_tron: {
    title: 'stable_tron',
    subtitle: 'TRON TRC-20 USDT transfers, large transfers (≥1M) & issue/redeem/freeze events',
  },
};

function extractErrStatusAndMessage(err: unknown): { status?: number; message: string } {
  if (axios.isAxiosError(err)) {
    const status = err.response?.status;
    const data = err.response?.data as { error?: string; message?: string } | string | undefined;
    const serverMsg =
      typeof data === 'string'
        ? data
        : data?.error || data?.message || err.message || '';
    return { status, message: mapStableBQError(status, serverMsg) };
  }
  if (err instanceof Error) {
    return { message: err.message };
  }
  return { message: 'BigQuery request failed.' };
}

export function StableBigQueryJobsPanel({
  bigquery,
  asOf,
  onClose,
  onRefreshMeta,
}: {
  bigquery?: StableMetaBigQuery | null;
  asOf?: string | null;
  onClose: () => void;
  onRefreshMeta: () => void;
}) {
  const [copiedJob, setCopiedJob] = useState<string | null>(null);
  const [estimatingJob, setEstimatingJob] = useState<string | null>(null);
  const [confirmingRun, setConfirmingRun] = useState(false);
  const [pendingEst, setPendingEst] = useState<StableBQEstimateResponse | null>(null);
  const [nothingToDoByJob, setNothingToDoByJob] = useState<Record<string, boolean>>({});
  const [liveMessage, setLiveMessage] = useState<string>('');
  const [errorMessage, setErrorMessage] = useState<string>('');

  const dialogRef = useRef<HTMLDialogElement | null>(null);
  const confirmBtnRef = useRef<HTMLButtonElement | null>(null);
  const cancelBtnRef = useRef<HTMLButtonElement | null>(null);
  const triggerBtnRefs = useRef<Record<string, HTMLButtonElement | null>>({});
  const lastTriggerJobRef = useRef<string | null>(null);
  const wasRunningRef = useRef<Record<string, boolean>>({});

  // Open/close native <dialog> and manage keyboard focus.
  useEffect(() => {
    const dlg = dialogRef.current;
    if (!dlg) return;
    if (pendingEst) {
      if (typeof dlg.showModal === 'function' && !dlg.open) {
        try {
          dlg.showModal();
        } catch {
          dlg.setAttribute('open', '');
        }
      } else {
        dlg.setAttribute('open', '');
      }
      const targetBtn = pendingEst.fits_budget ? confirmBtnRef.current : cancelBtnRef.current;
      targetBtn?.focus();
    } else if (dlg.open) {
      if (typeof dlg.close === 'function') {
        dlg.close();
      } else {
        dlg.removeAttribute('open');
      }
      if (lastTriggerJobRef.current) {
        triggerBtnRefs.current[lastTriggerJobRef.current]?.focus();
      }
    }
  }, [pendingEst]);

  // Announce completion when a job transitions from running=true to running=false.
  useEffect(() => {
    const statuses = bigquery?.statuses ?? {};
    for (const jobId of ['stable_eth', 'stable_tron']) {
      const st = statuses[jobId];
      const nowRunning = Boolean(st?.running);
      if (wasRunningRef.current[jobId] && !nowRunning && st) {
        setLiveMessage(
          `${jobId} finished (${st.last_run_result || st.state}): synced through ${st.synced_through || '—'} (${fmtBytesGiB(st.last_run_bytes_billed)} billed).`,
        );
      }
      wasRunningRef.current[jobId] = nowRunning;
    }
  }, [bigquery]);

  if (!bigquery) return null;

  const usedBytes =
    bigquery.budget_used_bytes ??
    Math.round(((bigquery.month_billed_gib ?? 0) + (bigquery.reserved_gib ?? 0)) * 1024 ** 3);
  const limitBytes =
    bigquery.budget_limit_bytes ?? Math.round((bigquery.monthly_budget_gib || 512) * 1024 ** 3);
  const usedGiB = usedBytes / 1024 ** 3;
  const limitGiB = limitBytes / 1024 ** 3;
  const budgetPct = limitBytes > 0 ? Math.min(100, (usedBytes / limitBytes) * 100) : 0;

  const statuses = bigquery.statuses ?? {};
  const nextNewDayIso =
    statuses.stable_eth?.next_new_day_at || statuses.stable_tron?.next_new_day_at || null;
  const yday = yesterdayUTC(asOf || nextNewDayIso);
  const anyJobRunning = Boolean(statuses.stable_eth?.running || statuses.stable_tron?.running);

  const handleCopySnippet = async (job: string) => {
    try {
      await navigator.clipboard?.writeText(CONF_SNIPPET);
      setCopiedJob(job);
      window.setTimeout(() => setCopiedJob(prev => (prev === job ? null : prev)), 2000);
    } catch {
      setCopiedJob(job);
    }
  };

  const handleRunNowClick = async (job: string) => {
    setErrorMessage('');
    setLiveMessage(`Running free dry-run estimate for ${job}…`);
    setEstimatingJob(job);
    lastTriggerJobRef.current = job;
    try {
      const est = await estimateStableBQJob(job);
      if (est.nothing_to_do) {
        setNothingToDoByJob(prev => ({ ...prev, [job]: true }));
        setLiveMessage(`${job} is already up to date through yesterday UTC.`);
        return;
      }
      setPendingEst(est);
      setLiveMessage(
        `Estimate ready for ${job}: ${fmtBytesGiB(est.estimated_bytes)} (~$${est.estimated_usd.toFixed(4)}). Review and confirm to start.`,
      );
    } catch (err) {
      const { message } = extractErrStatusAndMessage(err);
      setErrorMessage(message);
      setLiveMessage(`Estimate failed for ${job}: ${message}`);
    } finally {
      setEstimatingJob(null);
    }
  };

  const handleConfirmRun = async () => {
    if (!pendingEst) return;
    const job = pendingEst.job;
    const confirmedBytes = pendingEst.estimated_bytes;
    setConfirmingRun(true);
    setErrorMessage('');
    try {
      await runStableBQJob(job, confirmedBytes);
      setPendingEst(null);
      wasRunningRef.current[job] = true;
      setLiveMessage(`Started ${job} manual run — polling /meta every 5 s until completion…`);
      onRefreshMeta();
    } catch (err) {
      const { message } = extractErrStatusAndMessage(err);
      setErrorMessage(message);
      setLiveMessage(`Run refused for ${job}: ${message}`);
      setPendingEst(null);
      onRefreshMeta();
    } finally {
      setConfirmingRun(false);
    }
  };

  return (
    <section
      id="stable-bq-jobs-panel"
      aria-label="BigQuery jobs"
      className={`${cx.card} space-y-4`}
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <Database className="h-4 w-4 text-amber-600 dark:text-amber-400" aria-hidden="true" />
            <h2 className={cx.title}>BigQuery jobs</h2>
          </div>
          <p className={`mt-0.5 text-xs ${cx.muted}`}>
            New day available after 00:00 UTC
            {nextNewDayIso ? ` (next: ${fmtDateTime(nextNewDayIso)})` : ''}. Manual runs perform a free dry-run
            preview first and require confirmation on this computer (loopback 127.0.0.1 / ::1).
          </p>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close BigQuery jobs panel"
          className={`${cx.buttonSmall} ${cx.focus}`}
        >
          <X className="h-3.5 w-3.5" aria-hidden="true" />
          <span>Close</span>
        </button>
      </div>

      {/* Monthly budget bar */}
      <div className={`${cx.inset} space-y-1.5`}>
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
          <span className={`font-medium ${cx.strong}`}>
            Budget this month: {usedGiB.toFixed(2)} / {limitGiB.toFixed(0)} GiB
          </span>
          <span className={`font-mono text-[11px] ${cx.muted}`}>
            {fmtBytesGiB(usedBytes)} used (settled + open reservations) · {budgetPct.toFixed(1)}%
          </span>
        </div>
        <div
          role="progressbar"
          aria-label="BigQuery monthly budget used"
          aria-valuenow={Number(usedGiB.toFixed(2))}
          aria-valuemin={0}
          aria-valuemax={Number(limitGiB.toFixed(0))}
          className="h-2 w-full overflow-hidden rounded-full bg-zinc-200 dark:bg-zinc-800"
        >
          <div
            className={`h-full rounded-full transition-all ${
              budgetPct >= 90
                ? 'bg-red-600 dark:bg-red-500'
                : budgetPct >= 75
                  ? 'bg-amber-600 dark:bg-amber-500'
                  : 'bg-emerald-600 dark:bg-emerald-500'
            }`}
            style={{ width: `${Math.max(budgetPct, usedBytes > 0 ? 1.5 : 0)}%` }}
          />
        </div>
      </div>

      {/* Live status & error banner */}
      <div aria-live="polite" className="space-y-2">
        {(anyJobRunning || liveMessage) && (
          <div className="flex items-center gap-2 rounded-xl border border-sky-600/30 bg-sky-50 px-3 py-2 text-xs text-sky-900 dark:border-sky-500/30 dark:bg-sky-950/40 dark:text-sky-200">
            {anyJobRunning && (
              <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin motion-reduce:animate-none" aria-hidden="true" />
            )}
            <span>
              {anyJobRunning
                ? 'Run in progress — polling /meta every 5 s until finished…'
                : liveMessage}
            </span>
          </div>
        )}
        {errorMessage && (
          <div
            role="alert"
            className="rounded-xl border border-red-300 bg-red-50 px-3 py-2 text-xs text-red-800 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300"
          >
            {errorMessage}
          </div>
        )}
      </div>

      {/* Job rows */}
      <div className="grid gap-3 lg:grid-cols-2">
        {(['stable_eth', 'stable_tron'] as const).map(jobId => {
          const st = statuses[jobId];
          const meta = JOB_LABELS[jobId];
          const scheduleOn =
            st?.schedule_enabled ?? Boolean(bigquery.jobs && (jobId !== 'stable_tron' || bigquery.tron));
          const isRunning = Boolean(st?.running);
          const isStopped = st?.state === 'stopped';
          const syncedThru = st?.synced_through || '';
          const upToDate =
            Boolean(nothingToDoByJob[jobId]) || Boolean(yday && syncedThru && syncedThru >= yday);
          const disableRun = isRunning || isStopped || upToDate || estimatingJob === jobId;

          return (
            <div key={jobId} className={`${cx.inset} flex flex-col justify-between gap-3`}>
              <div className="space-y-2.5">
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div>
                    <div className={`font-mono text-xs font-semibold ${cx.strong}`}>{meta.title}</div>
                    <div className={`text-[11px] ${cx.muted}`}>{meta.subtitle}</div>
                  </div>
                  <button
                    ref={el => {
                      triggerBtnRefs.current[jobId] = el;
                    }}
                    type="button"
                    disabled={disableRun}
                    onClick={() => handleRunNowClick(jobId)}
                    className={`${cx.buttonSmall} ${cx.focus} font-semibold ${
                      disableRun ? 'cursor-not-allowed opacity-50' : 'cursor-pointer'
                    }`}
                  >
                    {isRunning || estimatingJob === jobId ? (
                      <Loader2 className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
                    ) : (
                      <Play className="h-3.5 w-3.5" aria-hidden="true" />
                    )}
                    <span>
                      {isRunning
                        ? 'Running…'
                        : estimatingJob === jobId
                          ? 'Estimating…'
                          : upToDate
                            ? `Up to date through ${syncedThru || yday || 'yesterday'}`
                            : 'Run now'}
                    </span>
                  </button>
                </div>

                {/* Schedule status + conf.yaml snippet */}
                <div className="rounded-lg border border-zinc-200/80 bg-white/80 p-2.5 text-xs dark:border-zinc-800 dark:bg-zinc-950/50">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className={cx.muted}>Schedule</span>
                    {scheduleOn ? (
                      <span className="font-medium text-emerald-700 dark:text-emerald-400">on (hourly)</span>
                    ) : (
                      <span className={`font-medium ${cx.text}`}>
                        off: set <code>stablecoin.bigquery.jobs: true</code> in conf.yaml
                      </span>
                    )}
                  </div>
                  {!scheduleOn && (
                    <div className="mt-2 flex items-center justify-between gap-2 rounded border border-zinc-200 bg-zinc-50 px-2.5 py-1.5 dark:border-zinc-800 dark:bg-zinc-900">
                      <pre className={`overflow-x-auto font-mono text-[11px] ${cx.text}`}>{CONF_SNIPPET}</pre>
                      <button
                        type="button"
                        onClick={() => handleCopySnippet(jobId)}
                        aria-label={`Copy conf.yaml snippet for ${jobId}`}
                        className={`${cx.buttonSmall} ${cx.focus} shrink-0`}
                      >
                        {copiedJob === jobId ? (
                          <>
                            <Check className="h-3 w-3 text-emerald-600 dark:text-emerald-400" aria-hidden="true" />
                            <span>Copied</span>
                          </>
                        ) : (
                          <>
                            <Copy className="h-3 w-3" aria-hidden="true" />
                            <span>Copy</span>
                          </>
                        )}
                      </button>
                    </div>
                  )}
                </div>

                <dl className="space-y-1 text-xs">
                  <div className="flex justify-between gap-2">
                    <dt className={cx.muted}>State</dt>
                    <dd className={`font-mono ${cx.text}`}>{isRunning ? 'running' : st?.state || 'off'}</dd>
                  </div>
                  <div className="flex justify-between gap-2">
                    <dt className={cx.muted}>Synced through</dt>
                    <dd className={`font-mono ${cx.text}`}>
                      {st?.synced_through || '—'}
                      {st?.coverage_from ? ` (from ${st.coverage_from})` : ''}
                    </dd>
                  </div>
                  <div className="flex justify-between gap-2">
                    <dt className={cx.muted}>Last run</dt>
                    <dd className={`font-mono text-right ${cx.text}`}>{fmtLastRunUTC(st)}</dd>
                  </div>
                  <div className="flex justify-between gap-2">
                    <dt className={cx.muted}>Next check</dt>
                    <dd className={`font-mono text-right ${cx.text}`}>{fmtNextCheckUTC(st?.next_check_at, asOf)}</dd>
                  </div>
                </dl>

                {isStopped && (
                  <div className="rounded-lg border border-red-600/30 bg-red-50 p-2 text-[11px] text-red-800 dark:border-red-500/30 dark:bg-red-950/40 dark:text-red-200">
                    <div className="font-medium">
                      Stopped: {st?.stop_reason || 'Repeated billed failures stopped this job.'}
                    </div>
                    <div className="mt-1 font-mono">
                      Resume via CLI: <code>./biglens-server --stablecoin-bq-resume {jobId}</code>
                    </div>
                  </div>
                )}
              </div>
            </div>
          );
        })}
      </div>

      {/* Native <dialog> confirmation modal */}
      <dialog
        ref={dialogRef}
        aria-labelledby="stable-bq-confirm-title"
        onCancel={e => {
          e.preventDefault();
          setPendingEst(null);
        }}
        className="m-auto w-full max-w-md rounded-2xl border border-zinc-200 bg-white p-5 text-zinc-900 shadow-xl backdrop:bg-black/50 dark:border-zinc-800 dark:bg-[#111114] dark:text-zinc-100"
      >
        {pendingEst && (
          <div className="space-y-4">
            <div className="flex items-start justify-between gap-2">
              <div>
                <h3 id="stable-bq-confirm-title" className="text-sm font-semibold">
                  Confirm BigQuery run ({pendingEst.job})
                </h3>
                <p className={`mt-0.5 text-xs ${cx.muted}`}>
                  Dry-run preview complete. Nothing is billed until you click Confirm.
                </p>
              </div>
              <button
                type="button"
                onClick={() => setPendingEst(null)}
                aria-label="Cancel BigQuery run"
                className={`${cx.buttonSmall} ${cx.focus}`}
              >
                <X className="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            </div>

            <dl className={`${cx.inset} space-y-1.5 text-xs`}>
              <div className="flex justify-between gap-2">
                <dt className={cx.muted}>Window to fetch (UTC days)</dt>
                <dd className={`font-mono font-semibold ${cx.strong}`}>
                  {pendingEst.window_from || '—'} → {pendingEst.window_to || '—'}
                </dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt className={cx.muted}>Estimated scan</dt>
                <dd className={`font-mono font-semibold ${cx.strong}`}>
                  {fmtBytesGiB(pendingEst.estimated_bytes)} (~${pendingEst.estimated_usd.toFixed(4)})
                </dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt className={cx.muted}>Budget used now</dt>
                <dd className={`font-mono ${cx.text}`}>
                  {fmtBytesGiB(pendingEst.budget_used_bytes)} /{' '}
                  {(pendingEst.budget_limit_bytes / 1024 ** 3).toFixed(0)} GiB
                </dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt className={cx.muted}>Budget after this run</dt>
                <dd className={`font-mono ${cx.text}`}>
                  {fmtBytesGiB(pendingEst.budget_used_bytes + pendingEst.estimated_bytes)} /{' '}
                  {(pendingEst.budget_limit_bytes / 1024 ** 3).toFixed(0)} GiB
                </dd>
              </div>
            </dl>

            {!pendingEst.fits_budget && (
              <div
                role="alert"
                className="rounded-xl border border-red-300 bg-red-50 px-3 py-2 text-xs text-red-800 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300"
              >
                This estimate exceeds the remaining monthly BigQuery budget and cannot be run.
              </div>
            )}

            <p className={`text-[11px] leading-relaxed ${cx.muted}`}>
              {(
                pendingEst.pricing_note ||
                'Estimated at $6.25/TiB; BigQuery bills actual bytes scanned.'
              ).replace(/\.?$/, '.')}{' '}
              If the fresh dry-run estimate grows by more than 10% before confirmation, the run is refused.
            </p>

            <div className="flex items-center justify-end gap-2 pt-1">
              <button
                ref={cancelBtnRef}
                type="button"
                onClick={() => setPendingEst(null)}
                className={`${cx.button} ${cx.focus}`}
              >
                Cancel
              </button>
              <button
                ref={confirmBtnRef}
                type="button"
                disabled={!pendingEst.fits_budget || confirmingRun}
                onClick={handleConfirmRun}
                className={`inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition ${cx.focus} ${
                  !pendingEst.fits_budget || confirmingRun
                    ? 'cursor-not-allowed bg-zinc-300 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-400'
                    : 'cursor-pointer bg-emerald-700 text-white hover:bg-emerald-800 dark:bg-emerald-500 dark:text-zinc-950 dark:hover:bg-emerald-400'
                }`}
              >
                {confirmingRun ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
                ) : (
                  <Play className="h-3.5 w-3.5" aria-hidden="true" />
                )}
                <span>Confirm &amp; run now</span>
              </button>
            </div>
          </div>
        )}
      </dialog>
    </section>
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

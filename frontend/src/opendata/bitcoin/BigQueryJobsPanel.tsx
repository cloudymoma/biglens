import { useEffect, useRef, useState } from 'react';
import axios from 'axios';
import { Check, Copy, Database, Loader2, Play, Terminal, X } from 'lucide-react';
import { estimateBitcoinBQJob, runBitcoinBQJob } from './api';
import {
  fmtBytesGiB,
  fmtDateTimeUTC,
  fmtLastRunUTC,
  fmtNextCheckUTC,
  mapBitcoinBQError,
  settledThroughUTC,
} from './format';
import type { BtcBaselineStatus, BtcBQEstimateResponse, BtcMetaBigQuery, BtcMetaCosts } from './types';

const RUNNABLE_JOBS = ['btc_onchain', 'btc_coinage'] as const;

const JOB_LABELS: Record<string, { title: string; subtitle: string }> = {
  btc_onchain: {
    title: 'btc_onchain',
    subtitle: 'Job A · daily rollup: fees, block space, script types, issuance and per-block heights (~0.65 GiB per day)',
  },
  btc_coinage: {
    title: 'btc_coinage',
    subtitle: 'Job B · spent-output age matrix, CDD, SOPR and dormant whales (~100 GiB per run, however short the window)',
  },
};

const SCHEDULE_SNIPPET = `bitcoin:
  bigquery:
    jobs: true`;

const COIN_AGE_SNIPPET = `bitcoin:
  bigquery:
    coin_age:
      enabled: true`;

const GIB = 1024 ** 3;

const cls = {
  card: 'rounded-xl border border-zinc-800 bg-zinc-900/40 p-5',
  inset: 'rounded-lg border border-zinc-800/80 bg-zinc-950/60 p-4',
  button:
    'inline-flex items-center gap-1.5 rounded-lg border border-zinc-800 bg-zinc-900/80 px-2.5 py-1.5 text-xs text-zinc-300 transition hover:border-zinc-700 hover:text-zinc-100',
  focus: 'focus:outline-none focus-visible:ring-2 focus-visible:ring-amber-400/70',
  muted: 'text-zinc-400',
  text: 'text-zinc-300',
  strong: 'text-zinc-100',
};

function extractErrMessage(err: unknown): string {
  if (axios.isAxiosError(err)) {
    const data = err.response?.data as { error?: string; message?: string } | string | undefined;
    const serverMsg = typeof data === 'string' ? data : data?.error || data?.message || err.message || '';
    return mapBitcoinBQError(err.response?.status, serverMsg);
  }
  if (err instanceof Error) return err.message;
  return 'BigQuery request failed.';
}

function CopySnippet({ snippet, label }: { snippet: string; label: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard?.writeText(snippet);
    } catch {
      // clipboard unavailable: the snippet stays selectable
    }
    setCopied(true);
    window.setTimeout(() => setCopied(false), 2000);
  };
  return (
    <div className="mt-2 flex items-center justify-between gap-2 rounded border border-zinc-800 bg-zinc-900 px-2.5 py-1.5">
      <pre className={`overflow-x-auto font-mono text-[11px] ${cls.text}`}>{snippet}</pre>
      <button type="button" onClick={copy} aria-label={label} className={`${cls.button} ${cls.focus} shrink-0`}>
        {copied ? (
          <>
            <Check className="h-3 w-3 text-emerald-400" aria-hidden="true" />
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
  );
}

function baselineState(b?: BtcBaselineStatus): string {
  if (!b) return '—';
  if (b.building) return 'building';
  if (b.pending) return 'built, handoff pending (next hourly tick)';
  if (b.available) return `available (generation ${b.generation})`;
  return 'not built';
}

// BaselineRow shows the UTXO baseline status only: it is a ~687 GiB run with a
// 30-day lock that stays on the CLI (or the allow_ui flow in Holders & Flows).
function BaselineRow({ baseline, costs }: { baseline?: BtcBaselineStatus; costs?: BtcMetaCosts }) {
  const cli = baseline?.cli_command || './biglens-server --bitcoin-utxo-baseline';
  const lockDays = baseline?.min_interval_days ?? 30;
  return (
    <div className={`${cls.inset} space-y-2.5`}>
      <div>
        <div className={`font-mono text-xs font-semibold ${cls.strong}`}>btc_utxo_baseline</div>
        <div className={`text-[11px] ${cls.muted}`}>
          Job C · full unspent UTXO snapshot by creation day: unlocks HODL waves, LTH/STH supply, realized cap and MVRV
        </div>
      </div>
      <dl className="grid gap-x-6 gap-y-1 text-xs sm:grid-cols-2">
        <div className="flex justify-between gap-2">
          <dt className={cls.muted}>State</dt>
          <dd className={`font-mono ${cls.text}`}>{baselineState(baseline)}</dd>
        </div>
        <div className="flex justify-between gap-2">
          <dt className={cls.muted}>As of</dt>
          <dd className={`font-mono ${cls.text}`}>{baseline?.as_of || '—'}</dd>
        </div>
        <div className="flex justify-between gap-2">
          <dt className={cls.muted}>Last built</dt>
          <dd className={`font-mono ${cls.text}`}>{fmtDateTimeUTC(baseline?.last_baseline_at || baseline?.built_at)}</dd>
        </div>
        <div className="flex justify-between gap-2">
          <dt className={cls.muted}>Next allowed</dt>
          <dd className={`font-mono ${cls.text}`}>
            {baseline?.next_allowed_at ? fmtDateTimeUTC(baseline.next_allowed_at) : 'any time'} ({lockDays}-day lock)
          </dd>
        </div>
        <div className="flex justify-between gap-2">
          <dt className={cls.muted}>Cost per run</dt>
          <dd className={`font-mono ${cls.text}`}>
            ~{(costs?.utxo_baseline_gib ?? 687.3).toFixed(0)} GiB (~${(costs?.utxo_baseline_usd ?? 4.2).toFixed(2)})
          </dd>
        </div>
      </dl>
      <div className="rounded border border-zinc-800 bg-zinc-900 px-2.5 py-1.5 font-mono text-[11px] text-zinc-300">
        <div className="flex items-center gap-1.5">
          <Terminal className="h-3 w-3 text-amber-400" aria-hidden="true" />
          <span>
            {cli} <span className="text-zinc-500"># free dry run</span>
          </span>
        </div>
        <div className="pl-[18px]">
          {cli} --yes <span className="text-zinc-500"># reserve budget and build</span>
        </div>
      </div>
      <p className={`text-[11px] leading-relaxed ${cls.muted}`}>
        Not run from this panel: the baseline runs from the operator shell
        {baseline?.allow_ui ? ' or the Holders & Flows tab (utxo_baseline.allow_ui: true)' : ''}, needs{' '}
        <code>bitcoin.bigquery.jobs: true</code>, and is locked for {lockDays} days between builds.
      </p>
    </div>
  );
}

export default function BigQueryJobsPanel({
  bigquery,
  onClose,
  onRefreshMeta,
}: {
  bigquery?: BtcMetaBigQuery | null;
  onClose: () => void;
  onRefreshMeta: () => void;
}) {
  const [estimatingJob, setEstimatingJob] = useState<string | null>(null);
  const [confirmingRun, setConfirmingRun] = useState(false);
  const [pendingEst, setPendingEst] = useState<BtcBQEstimateResponse | null>(null);
  // Maps job ID -> the settledThrough UTC day for which /estimate returned
  // nothing_to_do, so the "Up to date" state clears automatically once the
  // next UTC day settles at ~06:30 UTC.
  const [nothingToDoDayByJob, setNothingToDoDayByJob] = useState<Record<string, string>>({});
  const [liveMessage, setLiveMessage] = useState<string>('');
  const [errorMessage, setErrorMessage] = useState<string>('');

  const dialogRef = useRef<HTMLDialogElement | null>(null);
  const confirmBtnRef = useRef<HTMLButtonElement | null>(null);
  const cancelBtnRef = useRef<HTMLButtonElement | null>(null);
  const triggerBtnRefs = useRef<Record<string, HTMLButtonElement | null>>({});
  const lastTriggerJobRef = useRef<string | null>(null);
  const wasRunningRef = useRef<Record<string, boolean>>({});

  // Open/close the native <dialog> and manage keyboard focus.
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

  // Announce completion when a job goes from running to not running. /meta is
  // briefly null while it reloads: skip those renders so a reload cannot reset
  // the running flags and swallow the announcement.
  useEffect(() => {
    if (!bigquery) return;
    const statuses = bigquery.statuses ?? {};
    for (const jobId of RUNNABLE_JOBS) {
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
    bigquery.budget_used_bytes ?? Math.round(((bigquery.month_billed_gib ?? 0) + (bigquery.reserved_gib ?? 0)) * GIB);
  const limitBytes = bigquery.budget_limit_bytes ?? Math.round((bigquery.budget_gib || 2048) * GIB);
  const usedGiB = usedBytes / GIB;
  const limitGiB = limitBytes / GIB;
  const budgetPct = limitBytes > 0 ? Math.min(100, (usedBytes / limitBytes) * 100) : 0;

  const statuses = bigquery.statuses ?? {};
  const nextNewDayIso = statuses.btc_onchain?.next_new_day_at || statuses.btc_coinage?.next_new_day_at || null;
  const settledThrough = settledThroughUTC(nextNewDayIso);
  const anyJobRunning = RUNNABLE_JOBS.some(j => Boolean(statuses[j]?.running));
  const cadenceDays = bigquery.cadence_days || 7;

  const handleRunNowClick = async (job: string) => {
    setErrorMessage('');
    setLiveMessage(`Running free dry-run estimate for ${job}…`);
    setEstimatingJob(job);
    lastTriggerJobRef.current = job;
    try {
      const est = await estimateBitcoinBQJob(job);
      if (est.nothing_to_do) {
        if (settledThrough) {
          setNothingToDoDayByJob(prev => ({ ...prev, [job]: settledThrough }));
        }
        setLiveMessage(`${job} is already up to date through the latest settled UTC day.`);
        return;
      }
      setPendingEst(est);
      setLiveMessage(
        `Estimate ready for ${job}: ${fmtBytesGiB(est.estimated_bytes)} (~$${est.estimated_usd.toFixed(4)}). Review and confirm to start.`,
      );
    } catch (err) {
      const message = extractErrMessage(err);
      setErrorMessage(message);
      setLiveMessage(`Estimate failed for ${job}: ${message}`);
    } finally {
      setEstimatingJob(null);
    }
  };

  const handleConfirmRun = async () => {
    if (!pendingEst) return;
    const job = pendingEst.job;
    setConfirmingRun(true);
    setErrorMessage('');
    try {
      await runBitcoinBQJob(job, pendingEst.estimated_bytes);
      setPendingEst(null);
      wasRunningRef.current[job] = true;
      setLiveMessage(`Started ${job} manual run — polling /meta every 5 s until completion…`);
      onRefreshMeta();
    } catch (err) {
      const message = extractErrMessage(err);
      setErrorMessage(message);
      setLiveMessage(`Run refused for ${job}: ${message}`);
      setPendingEst(null);
      onRefreshMeta();
    } finally {
      setConfirmingRun(false);
    }
  };

  return (
    <section id="btc-bq-jobs-panel" aria-label="BigQuery jobs" className={`${cls.card} space-y-4`}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <Database className="h-4 w-4 text-amber-400" aria-hidden="true" />
            <h2 className={`text-base font-semibold ${cls.strong}`}>BigQuery jobs</h2>
          </div>
          <p className={`mt-0.5 text-xs ${cls.muted}`}>
            A UTC day settles after ~06:30 UTC (6 h block-timestamp slack)
            {nextNewDayIso ? ` (next: ${fmtDateTimeUTC(nextNewDayIso)})` : ''}. Manual runs perform a free dry-run preview
            first and require confirmation on this computer (loopback 127.0.0.1 / ::1).
          </p>
        </div>
        <button type="button" onClick={onClose} aria-label="Close BigQuery jobs panel" className={`${cls.button} ${cls.focus}`}>
          <X className="h-3.5 w-3.5" aria-hidden="true" />
          <span>Close</span>
        </button>
      </div>

      {/* Monthly budget bar (shared by Jobs A, B and C) */}
      <div className={`${cls.inset} space-y-1.5`}>
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
          <span className={`font-medium ${cls.strong}`}>
            Budget this month: {usedGiB.toFixed(2)} / {limitGiB.toFixed(0)} GiB
          </span>
          <span className={`font-mono text-[11px] ${cls.muted}`}>
            {fmtBytesGiB(usedBytes)} used (settled + open reservations) · {budgetPct.toFixed(1)}%
          </span>
        </div>
        <div
          role="progressbar"
          aria-label="BigQuery monthly budget used"
          aria-valuenow={Number(usedGiB.toFixed(2))}
          aria-valuemin={0}
          aria-valuemax={Number(limitGiB.toFixed(0))}
          className="h-2 w-full overflow-hidden rounded-full bg-zinc-800"
        >
          <div
            className={`h-full rounded-full transition-all ${
              budgetPct >= 90 ? 'bg-red-500' : budgetPct >= 75 ? 'bg-amber-500' : 'bg-emerald-500'
            }`}
            style={{ width: `${Math.max(budgetPct, usedBytes > 0 ? 1.5 : 0)}%` }}
          />
        </div>
        {bigquery.budget_warning && <p className="text-[11px] text-amber-300">{bigquery.budget_warning}</p>}
      </div>

      {/* Live status and error banner */}
      <div aria-live="polite" className="space-y-2">
        {(anyJobRunning || liveMessage) && (
          <div className="flex items-center gap-2 rounded-lg border border-sky-500/30 bg-sky-950/40 px-3 py-2 text-xs text-sky-200">
            {anyJobRunning && (
              <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin motion-reduce:animate-none" aria-hidden="true" />
            )}
            <span>{anyJobRunning ? 'Run in progress — polling /meta every 5 s until finished…' : liveMessage}</span>
          </div>
        )}
        {errorMessage && (
          <div role="alert" className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
            {errorMessage}
          </div>
        )}
      </div>

      {/* Runnable job rows */}
      <div className="grid gap-3 lg:grid-cols-2">
        {RUNNABLE_JOBS.map(jobId => {
          const st = statuses[jobId];
          const meta = JOB_LABELS[jobId];
          const isCoinage = jobId === 'btc_coinage';
          const coinAgeDisabled = isCoinage && bigquery.coin_age_enabled === false;
          const scheduleOn = st?.schedule_enabled ?? (Boolean(bigquery.jobs) && !coinAgeDisabled);
          const isRunning = Boolean(st?.running);
          const isStopped = st?.state === 'stopped';
          const syncedThru = st?.synced_through || '';
          const upToDate = Boolean(
            settledThrough &&
              ((syncedThru && syncedThru >= settledThrough) || nothingToDoDayByJob[jobId] === settledThrough),
          );
          const disableRun = isRunning || isStopped || coinAgeDisabled || upToDate || estimatingJob === jobId;

          return (
            <div key={jobId} className={`${cls.inset} flex flex-col justify-between gap-3`}>
              <div className="space-y-2.5">
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div>
                    <div className={`font-mono text-xs font-semibold ${cls.strong}`}>{meta.title}</div>
                    <div className={`text-[11px] ${cls.muted}`}>{meta.subtitle}</div>
                  </div>
                  <button
                    ref={el => {
                      triggerBtnRefs.current[jobId] = el;
                    }}
                    type="button"
                    disabled={disableRun}
                    onClick={() => handleRunNowClick(jobId)}
                    className={`${cls.button} ${cls.focus} font-semibold ${
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
                          : coinAgeDisabled
                            ? 'Disabled in conf.yaml'
                            : upToDate
                              ? `Up to date through ${syncedThru || settledThrough || 'the latest settled day'}`
                              : 'Run now'}
                    </span>
                  </button>
                </div>

                {/* Schedule status and conf.yaml snippet */}
                <div className="rounded-lg border border-zinc-800 bg-zinc-900/60 p-2.5 text-xs">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className={cls.muted}>Schedule</span>
                    {scheduleOn ? (
                      <span className="font-medium text-emerald-400">
                        {isCoinage ? `on (every ${cadenceDays} days)` : 'on (hourly)'}
                      </span>
                    ) : coinAgeDisabled ? (
                      <span className={`font-medium ${cls.text}`}>
                        off: set <code>bitcoin.bigquery.coin_age.enabled: true</code> in conf.yaml
                      </span>
                    ) : (
                      <span className={`font-medium ${cls.text}`}>
                        off: set <code>bitcoin.bigquery.jobs: true</code> in conf.yaml
                      </span>
                    )}
                  </div>
                  {!scheduleOn && (
                    <CopySnippet
                      snippet={coinAgeDisabled ? COIN_AGE_SNIPPET : SCHEDULE_SNIPPET}
                      label={`Copy conf.yaml snippet for ${jobId}`}
                    />
                  )}
                </div>

                <dl className="space-y-1 text-xs">
                  <div className="flex justify-between gap-2">
                    <dt className={cls.muted}>State</dt>
                    <dd className={`font-mono ${cls.text}`}>{isRunning ? 'running' : st?.state || 'off'}</dd>
                  </div>
                  <div className="flex justify-between gap-2">
                    <dt className={cls.muted}>Synced through</dt>
                    <dd className={`font-mono ${cls.text}`}>
                      {st?.synced_through || '—'}
                      {st?.coverage_from ? ` (from ${st.coverage_from})` : ''}
                    </dd>
                  </div>
                  <div className="flex justify-between gap-2">
                    <dt className={cls.muted}>Last run</dt>
                    <dd className={`text-right font-mono ${cls.text}`}>{fmtLastRunUTC(st)}</dd>
                  </div>
                  <div className="flex justify-between gap-2">
                    <dt className={cls.muted}>Next check</dt>
                    <dd className={`text-right font-mono ${cls.text}`}>{fmtNextCheckUTC(st?.next_check_at)}</dd>
                  </div>
                  {st?.state === 'retrying' && st.next_run_after && (
                    <div className="flex justify-between gap-2">
                      <dt className={cls.muted}>Retry after</dt>
                      <dd className="text-right font-mono text-amber-300">{fmtDateTimeUTC(st.next_run_after)}</dd>
                    </div>
                  )}
                </dl>

                {isCoinage && (
                  <p className={`text-[11px] leading-relaxed ${cls.muted}`}>
                    Run now fetches every settled day not yet synced; the schedule waits for {cadenceDays} new days
                    because each run scans about the same bytes however short the window.
                  </p>
                )}

                {isStopped && (
                  <div className="rounded-lg border border-red-500/30 bg-red-950/40 p-2 text-[11px] text-red-200">
                    <div className="font-medium">
                      Stopped: {st?.stop_reason || 'Repeated billed failures stopped this job.'}
                    </div>
                    <div className="mt-1 font-mono">
                      Resume via CLI: <code>./biglens-server --bitcoin-bq-resume {jobId}</code>
                    </div>
                  </div>
                )}
              </div>
            </div>
          );
        })}
      </div>

      <BaselineRow baseline={bigquery.baseline} costs={bigquery.costs} />

      {/* Native <dialog> confirmation modal */}
      <dialog
        ref={dialogRef}
        aria-labelledby="btc-bq-confirm-title"
        onCancel={e => {
          e.preventDefault();
          setPendingEst(null);
        }}
        className="m-auto w-full max-w-md rounded-2xl border border-zinc-800 bg-[#111114] p-5 text-zinc-100 shadow-xl backdrop:bg-black/60"
      >
        {pendingEst && (
          <div className="space-y-4">
            <div className="flex items-start justify-between gap-2">
              <div>
                <h3 id="btc-bq-confirm-title" className="text-sm font-semibold">
                  Confirm BigQuery run ({pendingEst.job})
                </h3>
                <p className={`mt-0.5 text-xs ${cls.muted}`}>
                  Dry-run preview complete. Nothing is billed until you click Confirm.
                </p>
              </div>
              <button
                type="button"
                onClick={() => setPendingEst(null)}
                aria-label="Cancel BigQuery run"
                className={`${cls.button} ${cls.focus}`}
              >
                <X className="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            </div>

            <dl className={`${cls.inset} space-y-1.5 text-xs`}>
              <div className="flex justify-between gap-2">
                <dt className={cls.muted}>Window to fetch (UTC days)</dt>
                <dd className={`font-mono font-semibold ${cls.strong}`}>
                  {pendingEst.window_from || '—'} → {pendingEst.window_to || '—'}
                </dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt className={cls.muted}>Estimated scan</dt>
                <dd className={`font-mono font-semibold ${cls.strong}`}>
                  {fmtBytesGiB(pendingEst.estimated_bytes)} (~${pendingEst.estimated_usd.toFixed(4)})
                </dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt className={cls.muted}>Reserved while running</dt>
                <dd className={`font-mono ${cls.text}`}>{fmtBytesGiB(pendingEst.reserve_bytes)} (1.25×, min 1 GiB)</dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt className={cls.muted}>Budget used now</dt>
                <dd className={`font-mono ${cls.text}`}>
                  {fmtBytesGiB(pendingEst.budget_used_bytes)} / {(pendingEst.budget_limit_bytes / GIB).toFixed(0)} GiB
                </dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt className={cls.muted}>Budget after this run</dt>
                <dd className={`font-mono ${cls.text}`}>
                  {fmtBytesGiB(pendingEst.budget_used_bytes + pendingEst.reserve_bytes)} /{' '}
                  {(pendingEst.budget_limit_bytes / GIB).toFixed(0)} GiB
                </dd>
              </div>
            </dl>

            {!pendingEst.fits_budget && (
              <div role="alert" className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">
                The budget reservation for this run exceeds the remaining monthly BigQuery budget (or the 256 GiB per-run
                cap), so it cannot run.
              </div>
            )}

            <p className={`text-[11px] leading-relaxed ${cls.muted}`}>
              {(pendingEst.pricing_note || 'Estimated at $6.25/TiB; BigQuery bills actual bytes scanned.').replace(
                /\.?$/,
                '.',
              )}{' '}
              If the fresh dry-run estimate grows by more than 10% before confirmation, the run is refused.
            </p>

            <div className="flex items-center justify-end gap-2 pt-1">
              <button
                ref={cancelBtnRef}
                type="button"
                onClick={() => setPendingEst(null)}
                className={`${cls.button} ${cls.focus}`}
              >
                Cancel
              </button>
              <button
                ref={confirmBtnRef}
                type="button"
                disabled={!pendingEst.fits_budget || confirmingRun}
                onClick={handleConfirmRun}
                className={`inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-semibold transition ${cls.focus} ${
                  !pendingEst.fits_budget || confirmingRun
                    ? 'cursor-not-allowed bg-zinc-800 text-zinc-400'
                    : 'cursor-pointer bg-amber-500 text-zinc-950 hover:bg-amber-400'
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

import { useEffect, useEffectEvent, useRef, useState } from 'react';
import type {
  AddressRiskBackfillStatus,
  AddressRiskChain,
  AddressRiskLookup,
  AddressRiskSources,
} from '../../types';
import {
  fetchAddressRiskBackfill,
  fetchAddressRiskLookup,
  fetchAddressRiskSources,
  postAddressRiskBackfill,
} from '../../api';
import { ErrorBanner } from '../../dashboards/shared';
import { fmtNum, Panel } from './shared';
import {
  ago,
  detectAddressFamily,
  isValidAddressForChain,
  RISK_CHAINS,
  RISK_SOURCE_LABELS as SOURCE_LABELS,
  sourceState,
} from './addressRiskTools';
import AddressRiskOverview from './AddressRiskOverview';
import EtherscanKeyPanel from './EtherscanKeyPanel';
import RiskResultView from './RiskResultView';

// GB (1e9), as the backend and CLI print it.
function fmtGB(bytes: number): string {
  return `${(bytes / 1e9).toFixed(1)} GB`;
}

// The last dry-run estimate; confirm sends its token back (valid 15 min, single use).
interface DryRunPlan {
  token: string;
  force: boolean;
  bytes: number;
  usd: number;
  batches: number;
}

function buildThirdPartyNote(
  chain: AddressRiskChain,
  chainLabel: string,
  result: AddressRiskLookup | null,
  meta: AddressRiskSources | null,
): string {
  if (result && (result.chain || 'eth') === chain) {
    const sent = result.sources.filter(s => s.sends_address && s.hosts && s.hosts.length > 0);
    const local = result.sources.filter(s => !s.sends_address).map(s => SOURCE_LABELS[s.id] ?? s.id);
    const localText = local.length > 0 ? `${local.join(', ')} checks run on this server.` : '';
    if (sent.length === 0) {
      return `Lookups on ${chainLabel} do not send this address to any third party. ${localText}`.trim();
    }
    const hosts = Array.from(new Set(sent.flatMap(s => s.hosts ?? [])));
    const hopNote = chain === 'eth' ? ' (and up to 4 top counterparties during 1-hop screening)' : '';
    return `Lookups on ${chainLabel} send this address${hopNote} to: ${hosts.join(', ')}. ${localText}`.trim();
  }

  const rpcHosts = meta?.rpc_hosts.join(', ') || 'public Ethereum RPCs';
  const goplusHost = meta?.goplus_host || 'api.gopluslabs.io';
  switch (chain) {
    case 'btc':
      return 'Lookups on Bitcoin do not send this address to any third party. OFAC checks run on this server.';
    case 'sol':
      return `Lookups on Solana send this address to configured Solana RPCs (SPL freeze check) and ${goplusHost}. OFAC checks run on this server.`;
    case 'tron':
      return `Lookups on TRON send this address to TronGrid (issuer freeze) and ${goplusHost}. OFAC checks run on this server.`;
    case 'base':
      return `Lookups on Base send this address to configured Base RPCs (issuer freeze), ${goplusHost}, and base.blockscout.com. OFAC and MEW checks run on this server.`;
    case 'arb':
    case 'op': {
      const bsHost = chain === 'arb' ? 'arbitrum.blockscout.com' : 'explorer.optimism.io';
      return `Lookups on ${chainLabel} send this address to configured ${chainLabel} RPCs (issuer freeze & Chainalysis oracle), ${goplusHost}, and ${bsHost}. OFAC and MEW checks run on this server.`;
    }
    default:
      return (
        `Lookups send this address (and up to 4 top counterparties during 1-hop screening) to: ${rpcHosts} ` +
        `(issuer freeze & Chainalysis oracle; tried in order), ${goplusHost}, eth.blockscout.com` +
        `${meta?.etherscan?.configured ? ', and api.etherscan.io' : ' (also used as keyless 1-hop fallback)'}. ` +
        'OFAC, MEW and stablecoin checks run on this server.'
      );
  }
}

export default function AddressRiskTab({
  inspect: inspectReq,
}: {
  inspect?: { address: string; chain?: AddressRiskChain; seq: number };
}) {
  const [chain, setChain] = useState<AddressRiskChain>('eth');
  const [input, setInput] = useState('');
  const [inputError, setInputError] = useState('');
  const [result, setResult] = useState<AddressRiskLookup | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [meta, setMeta] = useState<AddressRiskSources | null>(null);
  const [backfill, setBackfill] = useState<AddressRiskBackfillStatus | null>(null);
  const [backfillBusy, setBackfillBusy] = useState(false);
  const [backfillErr, setBackfillErr] = useState('');
  const [dryRunPlan, setDryRunPlan] = useState<DryRunPlan | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const resultRef = useRef<HTMLHeadingElement | null>(null);

  const refreshMeta = () => {
    fetchAddressRiskSources().then(setMeta).catch(() => setMeta(null));
    fetchAddressRiskBackfill().then(setBackfill).catch(() => setBackfill(null));
  };
  useEffect(refreshMeta, []);

  // Poll backfill status every 3s while a background backfill is running.
  useEffect(() => {
    if (!backfill?.running) return;
    const timer = window.setInterval(() => {
      fetchAddressRiskBackfill()
        .then(st => {
          setBackfill(st);
          if (!st.running) {
            fetchAddressRiskSources().then(setMeta).catch(() => setMeta(null));
          }
        })
        .catch(() => {});
    }, 3000);
    return () => window.clearInterval(timer);
  }, [backfill?.running]);

  const requestDryRun = (force = false) => {
    setBackfillBusy(true);
    setBackfillErr('');
    postAddressRiskBackfill({ dry_run: true, force })
      .then(st => {
        setBackfill(st);
        if (st.dry_run_ready && st.dry_run_token) {
          setDryRunPlan({
            token: st.dry_run_token,
            force,
            bytes: st.dry_run_bytes ?? 0,
            usd: st.dry_run_usd ?? 0,
            batches: st.dry_run_batches ?? 0,
          });
        }
      })
      .catch(e => {
        const d = e.response?.data;
        setBackfillErr(typeof d === 'string' && d ? d : e.message);
      })
      .finally(() => setBackfillBusy(false));
  };

  const confirmBackfill = (plan: DryRunPlan) => {
    setBackfillBusy(true);
    setBackfillErr('');
    setDryRunPlan(null);
    postAddressRiskBackfill({ confirm: true, force: plan.force, dry_run_token: plan.token })
      .then(st => {
        setBackfill(st);
        refreshMeta();
      })
      .catch(e => {
        const d = e.response?.data;
        setBackfillErr(typeof d === 'string' && d ? d : e.message);
      })
      .finally(() => setBackfillBusy(false));
  };

  const lookup = (raw: string, targetChain: AddressRiskChain = chain) => {
    const addr = raw.trim();
    const chainOpt = RISK_CHAINS.find(c => c.id === targetChain) ?? RISK_CHAINS[0];
    if (!isValidAddressForChain(targetChain, addr)) {
      setInputError(chainOpt.formatHint);
      return;
    }
    setInputError('');
    abortRef.current?.abort(); // a newer lookup supersedes the in-flight one
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    setResult(null); // never leave a previous address's clues on screen while loading
    setError('');
    setLoading(true);
    fetchAddressRiskLookup(addr, targetChain, ctrl.signal)
      .then(r => {
        if (ctrl.signal.aborted || r.address.toLowerCase() !== addr.toLowerCase()) return;
        setResult(r);
        refreshMeta(); // source status chips must not freeze at mount time
        requestAnimationFrame(() => resultRef.current?.focus());
      })
      .catch(e => {
        if (ctrl.signal.aborted) return;
        // writeError returns text/plain; a proxy 502/504 may return JSON or HTML
        // objects, which must not be rendered as a React child.
        const d = e.response?.data;
        setError(typeof d === 'string' && d ? d : e.message);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });
  };

  const inspect = (addr: string, targetChain: AddressRiskChain = 'eth') => {
    setChain(targetChain);
    setInput(addr);
    lookup(addr, targetChain);
  };

  // An address clicked in Whales & Flow (spec §11.4). The tab stays mounted,
  // so the lookup is driven by seq, not by mounting.
  const onInspect = useEffectEvent(() => {
    if (inspectReq?.address) inspect(inspectReq.address, inspectReq.chain ?? 'eth');
  });
  const inspectSeq = inspectReq?.seq ?? 0;
  useEffect(() => {
    // The parent's click is an external event this effect applies; running
    // the lookup here is the spec §11.4 design (seq-driven, tab stays mounted).
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (inspectSeq > 0) onInspect();
  }, [inspectSeq]);

  const selectedChainOpt = RISK_CHAINS.find(c => c.id === chain) ?? RISK_CHAINS[0];
  const detectedFamily = detectAddressFamily(input);
  const familyMismatch = detectedFamily !== null && detectedFamily !== selectedChainOpt.family ? detectedFamily : null;

  const selectChain = (nextChain: AddressRiskChain) => {
    if (nextChain !== chain) {
      abortRef.current?.abort();
      setLoading(false);
      setResult(null);
      setError('');
    }
    setChain(nextChain);
    setInputError('');
  };

  return (
    <div className="space-y-4">
      {backfill && (
        <div className="rounded-xl border border-zinc-800 bg-zinc-900/40 px-4 py-3 flex flex-wrap items-center justify-between gap-3 text-xs">
          <div className="space-y-0.5">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-semibold text-zinc-200">USDT / USDC Freeze History Backfill</span>
              {backfill.running ? (
                <span className="rounded px-1.5 py-0.5 text-[10px] font-medium bg-amber-500/15 text-amber-300 border border-amber-500/30">
                  Running · {backfill.progress_label || 'in progress'}
                </span>
              ) : backfill.corrupted ? (
                <span className="rounded px-1.5 py-0.5 text-[10px] font-medium bg-red-500/15 text-red-300 border border-red-500/30">
                  Data Integrity Alert
                </span>
              ) : backfill.completed ? (
                <span className="rounded px-1.5 py-0.5 text-[10px] font-medium bg-emerald-500/15 text-emerald-300 border border-emerald-500/30">
                  Completed & Persisted
                </span>
              ) : backfill.status === 'failed' ? (
                <span className="rounded px-1.5 py-0.5 text-[10px] font-medium bg-orange-500/15 text-orange-300 border border-orange-500/30">
                  Interrupted
                </span>
              ) : (
                <span className="rounded px-1.5 py-0.5 text-[10px] font-medium bg-zinc-800 text-zinc-400">
                  Partial ({backfill.coverage_from || '30d'} → {backfill.coverage_to || 'now'})
                </span>
              )}
            </div>
            <p className="text-[11px] text-zinc-500">
              {backfill.completed && !backfill.corrupted ? (
                <>
                  Full history synced ({backfill.since_date || '2017-11-28'} → {backfill.coverage_to || backfill.through_date}) ·{' '}
                  <span className="text-zinc-300">{fmtNum(backfill.live_events)}</span> events stored (USDT {fmtNum(backfill.usdt_events)} · USDC {fmtNum(backfill.usdc_events)})
                  {backfill.completed_at ? ` · completed ${ago(backfill.completed_at)}` : ''}
                </>
              ) : backfill.corrupted ? (
                <span className="text-orange-400">
                  Integrity check failed: {backfill.corrupt_reason}. Re-run backfill to restore missing historical records.
                </span>
              ) : backfill.running ? (
                <>
                  Backfilling from 2017-11-28 via BigQuery… {fmtNum(backfill.live_events)} events stored so far (USDT {fmtNum(backfill.usdt_events)} · USDC {fmtNum(backfill.usdc_events)}).
                </>
              ) : (
                <>
                  Local DB currently holds {fmtNum(backfill.live_events)} events ({backfill.coverage_from || 'recent window'} → {backfill.coverage_to || 'now'}). Backfill once to persist full freeze history since 2017-11-28.
                </>
              )}
            </p>
            {(backfill.error || backfillErr) && (
              <p className="text-[11px] text-orange-400">{backfillErr || backfill.error}</p>
            )}
          </div>

          <div className="flex items-center gap-2">
            {backfill.running ? (
              <button
                type="button"
                disabled
                className="px-3 py-1.5 rounded-lg text-xs font-medium bg-zinc-800 text-zinc-400 opacity-60 cursor-not-allowed"
              >
                Backfilling…
              </button>
            ) : dryRunPlan ? (
              <div className="flex flex-wrap items-center gap-2 text-[11px]">
                <span className="text-zinc-300">
                  Dry-run estimate: ~{fmtGB(dryRunPlan.bytes)} (~${dryRunPlan.usd.toFixed(2)} across {dryRunPlan.batches} batch{dryRunPlan.batches === 1 ? '' : 'es'}; valid 15 min)
                </span>
                <button
                  type="button"
                  disabled={backfillBusy}
                  onClick={() => confirmBackfill(dryRunPlan)}
                  title="The server re-checks the estimate and stops before billing if it grew by more than 10%."
                  className="px-2.5 py-1 rounded-md bg-amber-500/20 text-amber-200 border border-amber-500/30 hover:bg-amber-500/30 disabled:opacity-50"
                >
                  {backfillBusy ? 'Starting…' : 'Confirm & Start Backfill'}
                </button>
                <button
                  type="button"
                  disabled={backfillBusy}
                  onClick={() => setDryRunPlan(null)}
                  className="text-zinc-400 hover:text-zinc-200 underline"
                >
                  Cancel
                </button>
              </div>
            ) : !backfill.can_run ? (
              <>
                <button
                  type="button"
                  disabled
                  title="Full history since 2017-11-28 is already persisted and verified in SQLite"
                  className="px-3 py-1.5 rounded-lg text-xs font-medium bg-zinc-800/60 text-zinc-500 border border-zinc-800 cursor-not-allowed"
                >
                  Backfill Complete
                </button>
                <button
                  type="button"
                  disabled={backfillBusy}
                  onClick={() => requestDryRun(true)}
                  className="text-[11px] text-zinc-500 hover:text-zinc-300 underline decoration-zinc-700 disabled:opacity-50"
                >
                  {backfillBusy ? 'Estimating…' : 'Force re-sync'}
                </button>
              </>
            ) : (
              <button
                type="button"
                disabled={backfillBusy}
                onClick={() => requestDryRun(backfill.corrupted)}
                className="px-3 py-1.5 rounded-lg text-xs font-medium bg-zinc-800 hover:bg-zinc-700 text-white border border-zinc-700 disabled:opacity-50"
              >
                {backfillBusy
                  ? 'Estimating…'
                  : backfill.corrupted
                    ? 'Repair & Re-backfill'
                    : backfill.status === 'failed'
                      ? 'Resume Backfill'
                      : 'Backfill Full History (since 2017-11-28)'}
              </button>
            )}
          </div>
        </div>
      )}

      <Panel title="Address risk clues" note="Ethereum, Arbitrum, Optimism, Base, TRON, Solana · Bitcoin: OFAC only">
        <div className="mb-2.5 flex flex-wrap items-center gap-1.5" role="group" aria-label="Chain">
          {RISK_CHAINS.map(c => {
            const active = chain === c.id;
            return (
              <button
                key={c.id}
                type="button"
                onClick={() => selectChain(c.id)}
                className={`px-2.5 py-1 rounded-lg text-xs font-medium transition-colors border ${
                  active
                    ? 'bg-zinc-800 text-white border-zinc-700'
                    : 'bg-zinc-900/50 text-zinc-400 border-zinc-800/80 hover:text-zinc-200 hover:border-zinc-700'
                }`}
              >
                {c.label}{active ? ' ✓' : ''}
              </button>
            );
          })}
        </div>

        <form
          className="flex gap-2"
          onSubmit={e => { e.preventDefault(); lookup(input, chain); }}
        >
          <input
            value={input}
            onChange={e => setInput(e.target.value)}
            placeholder={selectedChainOpt.placeholder}
            spellCheck={false}
            aria-label={`${selectedChainOpt.label} address`}
            className="flex-1 rounded-lg bg-zinc-900 border border-zinc-800 px-3 py-2 text-sm font-mono text-zinc-200 focus:outline-none focus:border-zinc-600"
          />
          <button
            type="submit"
            disabled={loading}
            className="px-4 py-2 rounded-lg text-sm font-medium bg-zinc-800 text-white disabled:opacity-50"
          >
            {loading ? 'Checking…' : 'Check'}
          </button>
        </form>

        {familyMismatch === 'tron' && (
          <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-amber-300">
            <span>This looks like a TRON address — switch to TRON?</span>
            <button
              type="button"
              onClick={() => selectChain('tron')}
              className="px-2 py-0.5 rounded bg-amber-500/20 text-amber-200 border border-amber-500/30 hover:bg-amber-500/30"
            >
              Switch to TRON
            </button>
          </div>
        )}
        {familyMismatch === 'btc' && (
          <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-amber-300">
            <span>This looks like a Bitcoin address — switch to Bitcoin?</span>
            <button
              type="button"
              onClick={() => selectChain('btc')}
              className="px-2 py-0.5 rounded bg-amber-500/20 text-amber-200 border border-amber-500/30 hover:bg-amber-500/30"
            >
              Switch to Bitcoin
            </button>
          </div>
        )}
        {familyMismatch === 'sol' && (
          <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-amber-300">
            <span>This looks like a Solana address — switch to Solana?</span>
            <button
              type="button"
              onClick={() => selectChain('sol')}
              className="px-2 py-0.5 rounded bg-amber-500/20 text-amber-200 border border-amber-500/30 hover:bg-amber-500/30"
            >
              Switch to Solana
            </button>
          </div>
        )}
        {familyMismatch === 'evm' && (
          <div className="mt-2 flex flex-wrap items-center gap-1.5 text-xs text-amber-300">
            <span>This looks like an EVM address — switch to Ethereum?</span>
            {RISK_CHAINS.filter(c => c.family === 'evm').map(c => (
              <button
                key={c.id}
                type="button"
                onClick={() => selectChain(c.id)}
                className="px-2 py-0.5 rounded bg-amber-500/20 text-amber-200 border border-amber-500/30 hover:bg-amber-500/30"
              >
                Switch to {c.label}
              </button>
            ))}
          </div>
        )}

        {inputError && <p className="mt-2 text-xs text-orange-400">{inputError}</p>}
        <p className="mt-3 text-[11px] text-zinc-500">
          {buildThirdPartyNote(chain, selectedChainOpt.label, result, meta)}
        </p>
        {meta && (
          <div className="mt-2 flex flex-wrap gap-2 text-[11px] text-zinc-500">
            {meta.lists
              .filter(s => selectedChainOpt.localSources.includes(s.id))
              .map(s => (
                <span key={s.id} className="rounded-md border border-zinc-800 px-2 py-0.5">
                  {SOURCE_LABELS[s.id] ?? s.id} · {sourceState(s)}{s.coverage_from ? ` · since ${s.coverage_from}` : ''}{s.last_ok_at ? ` · fetched ${ago(s.last_ok_at)}` : ''}
                </span>
              ))}
          </div>
        )}
        {chain === 'eth' && <EtherscanKeyPanel info={meta?.etherscan ?? null} onChange={refreshMeta} />}
      </Panel>

      {error && <ErrorBanner message={error} />}

      {result && <RiskResultView result={result} headingRef={resultRef} />}

      <AddressRiskOverview sources={meta} onInspect={addr => inspect(addr, 'eth')} />
    </div>
  );
}

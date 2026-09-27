import { useEffect, useEffectEvent, useRef, useState } from 'react';
import { ExternalLink } from 'lucide-react';
import type {
  AddressRiskBackfillStatus,
  AddressRiskLookup,
  AddressRiskScope,
  AddressRiskSeverity,
  AddressRiskSource,
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
import { RISK_SOURCE_LABELS as SOURCE_LABELS, RISK_TOOLS, REVOKE_CASH_URL } from './addressRiskTools';
import AddressRiskOverview from './AddressRiskOverview';
import EtherscanKeyPanel from './EtherscanKeyPanel';

const ADDRESS_RE = /^0x[0-9a-fA-F]{40}$/;

// Severity is always shown as text + count; color is a secondary cue only.
const GROUPS: { id: AddressRiskSeverity; label: string; color: string }[] = [
  { id: 'critical', label: 'Critical', color: '#f87171' },
  { id: 'warning', label: 'Warning', color: '#fb923c' },
  { id: 'association', label: 'Association', color: '#fde047' },
  { id: 'info', label: 'Info', color: '#a1a1aa' },
];

const ERROR_TEXT: Record<string, string> = {
  timeout: 'timeout',
  network_error: 'network error',
  rate_limited: 'rate limited',
  pending: 'data pending',
  bad_response: 'unexpected response',
  unavailable: 'local database unavailable',
  key_invalid: 'API key rejected',
  key_throttled: 'key checks throttled',
  rate_limited_local: 'local rate limit reached',
  local_pool_unavailable: 'local lists unavailable',
};

// Query health in neutral words — never a green check (spec §8.2).
function sourceState(s: AddressRiskSource): string {
  switch (s.status) {
    case 'ok': return 'checked';
    case 'stale': return 'stale data';
    case 'empty': return 'not synced yet';
    case 'partial': return 'partial coverage';
    case 'not_configured': return 'not configured';
    default: {
      const e = s.error ?? '';
      if (e.startsWith('upstream_http_')) return `upstream HTTP ${e.slice('upstream_http_'.length)}`;
      return ERROR_TEXT[e] ?? 'error';
    }
  }
}

const ETHERSCAN_PAGE = 1000;
const SCOPE_LABELS: ['txlist' | 'tokentx' | 'txlistinternal', string][] = [
  ['txlist', 'Transactions'], ['tokentx', 'Token transfers'], ['txlistinternal', 'Internal transfers'],
];

// What the association analysis actually covered, per list (spec §8.4): for
// a busy address 1000 rows can be only a few hours.
function associationNotes(r: AddressRiskLookup): string[] {
  const es = r.sources.find(s => s.id === 'etherscan');
  if (!es || es.status === 'not_configured') {
    return ['Not checked: add a free Etherscan API key above to see transfers with listed addresses.'];
  }
  const sc: AddressRiskScope | null = r.association_scope;
  if (es.status !== 'ok' || !sc) return [`Not checked: ${es.hosts?.includes('eth.blockscout.com') ? 'Blockscout' : 'Etherscan'} ${sourceState(es)}${es.last_error ? ` (${es.last_error})` : ''}.`];
  const lines = SCOPE_LABELS.map(([k, label]) => {
    const l = sc[k];
    return l.n < ETHERSCAN_PAGE ? `${label}: all ${l.n}` : `${label}: latest ${l.n} (since ${l.oldest_at.slice(0, 10)})`;
  });
  lines.push(`${sc.hops} hop · tokens: ${sc.token_allowlist.join(', ')}`);
  if (sc.counterparties_screened > 0) {
    const errSuffix = sc.counterparty_errors
      ? ` (${sc.counterparty_errors} live check error${sc.counterparty_errors === 1 ? '' : 's'})`
      : '';
    lines.push(
      `Live counterparty screening: ${sc.counterparties_screened} top counterpart${sc.counterparties_screened === 1 ? 'y' : 'ies'} checked${errSuffix}.`,
    );
  }
  if (sc.truncated) lines.push('Showing the first 200 counterparties.');
  return lines;
}

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

function ago(iso: string): string {
  const mins = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 60000));
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins} min ago`;
  const h = Math.round(mins / 60);
  return h < 48 ? `${h} h ago` : `${Math.round(h / 24)} days ago`;
}

export default function AddressRiskTab({ inspect: inspectReq }: { inspect?: { address: string; seq: number } }) {
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

  const lookup = (raw: string) => {
    const addr = raw.trim();
    if (!ADDRESS_RE.test(addr)) {
      setInputError('Enter a 0x address (42 characters). ENS names are not supported.');
      return;
    }
    setInputError('');
    abortRef.current?.abort(); // a newer lookup supersedes the in-flight one
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    setResult(null); // never leave a previous address's clues on screen while loading
    setError('');
    setLoading(true);
    fetchAddressRiskLookup(addr, ctrl.signal)
      .then(r => {
        if (ctrl.signal.aborted || r.address !== addr.toLowerCase()) return;
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

  const inspect = (addr: string) => {
    setInput(addr);
    lookup(addr);
  };

  // An address clicked in Whales & Flow (spec §11.4). The tab stays mounted,
  // so the lookup is driven by seq, not by mounting.
  const onInspect = useEffectEvent(() => {
    if (inspectReq?.address) inspect(inspectReq.address);
  });
  const inspectSeq = inspectReq?.seq ?? 0;
  useEffect(() => {
    // The parent's click is an external event this effect applies; running
    // the lookup here is the spec §11.4 design (seq-driven, tab stays mounted).
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (inspectSeq > 0) onInspect();
  }, [inspectSeq]);

  const rpcHosts = meta?.rpc_hosts.join(', ') || 'public Ethereum RPCs';
  const goplusHost = meta?.goplus_host || 'api.gopluslabs.io';
  const esSource = result?.sources.find(s => s.id === 'etherscan');
  const usedBlockscoutFallback = Boolean(esSource?.hosts?.includes('eth.blockscout.com'));

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

      <Panel title="Ethereum address risk clues" note="Ethereum mainnet only">
        <form
          className="flex gap-2"
          onSubmit={e => { e.preventDefault(); lookup(input); }}
        >
          <input
            value={input}
            onChange={e => setInput(e.target.value)}
            placeholder="0x… address"
            spellCheck={false}
            aria-label="Ethereum address"
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
        {inputError && <p className="mt-2 text-xs text-orange-400">{inputError}</p>}
        <p className="mt-3 text-[11px] text-zinc-500">
          Lookups send this address (and up to 4 top counterparties during 1-hop screening) to: {rpcHosts} (Chainalysis oracle; tried in order), {goplusHost}, eth.blockscout.com{meta?.etherscan?.configured ? ', and api.etherscan.io' : ' (also used as keyless 1-hop fallback)'}. OFAC, MEW
          and stablecoin checks run on this server.
        </p>
        {meta && (
          <div className="mt-2 flex flex-wrap gap-2 text-[11px] text-zinc-500">
            {meta.lists.map(s => (
              <span key={s.id} className="rounded-md border border-zinc-800 px-2 py-0.5">
                {SOURCE_LABELS[s.id] ?? s.id} · {sourceState(s)}{s.coverage_from ? ` · since ${s.coverage_from}` : ''}{s.last_ok_at ? ` · fetched ${ago(s.last_ok_at)}` : ''}
              </span>
            ))}
          </div>
        )}
        <EtherscanKeyPanel info={meta?.etherscan ?? null} onChange={refreshMeta} />
      </Panel>

      {error && <ErrorBanner message={error} />}

      {result && (
        <Panel title="Result" note={`checked ${ago(result.queried_at)}`}>
          <h4 ref={resultRef} tabIndex={-1} className="font-mono text-sm text-white break-all outline-none">
            {result.address}
          </h4>
          {result.checksum_warning && (
            <p className="mt-2 text-xs text-orange-400">
              The address's upper/lower-case checksum (EIP-55) does not match — check it for typos. Results are for the lowercase address.
            </p>
          )}
          <p role="status" className="mt-3 text-sm text-zinc-200">{result.summary.text}</p>
          <p className="mt-1 text-[11px] text-zinc-500">{result.disclaimer}</p>

          <div className="mt-4 space-y-4">
            {GROUPS.map(g => {
              const clues = result.clues.filter(c => c.severity === g.id);
              if (clues.length === 0 && g.id !== 'association') return null;
              return (
                <section key={g.id}>
                  <h5 className="text-xs font-semibold" style={{ color: g.color }}>
                    {g.label} ({clues.length})
                  </h5>
                  {g.id === 'association' && (
                    <div className="mt-1 text-[11px] text-zinc-500">
                      {usedBlockscoutFallback ? (
                        <a href="https://eth.blockscout.com" target="_blank" rel="noopener noreferrer" className="text-zinc-400 hover:text-zinc-200">
                          Data provided by Blockscout (keyless fallback)
                        </a>
                      ) : (
                        <a href="https://etherscan.io" target="_blank" rel="noopener noreferrer" className="text-zinc-400 hover:text-zinc-200">
                          Data provided by Etherscan
                        </a>
                      )}
                      {associationNotes(result).map(n => <p key={n}>{n}</p>)}
                    </div>
                  )}
                  <ul className="mt-2 space-y-2">
                    {clues.map((c, i) => (
                      <li key={`${c.source}-${c.flag}-${i}`} className="rounded-lg border border-zinc-800 p-3 text-xs">
                        <div className="flex items-start justify-between gap-3">
                          <span className="text-zinc-200">
                            <span className="text-zinc-500">{g.label} · </span>{c.title}
                          </span>
                          {c.ref_url && (
                            <a href={c.ref_url} target="_blank" rel="noopener noreferrer"
                               className="shrink-0 text-zinc-400 hover:text-zinc-200 inline-flex items-center gap-1">
                              source <ExternalLink size={11} />
                            </a>
                          )}
                        </div>
                        {c.detail && <p className="mt-1 text-zinc-400">{c.detail}</p>}
                        <p className="mt-1 text-[11px] text-zinc-600">
                          {SOURCE_LABELS[c.source] ?? c.source}
                          {c.association ? ` · ${c.association.tx_count} tx · latest ${c.association.amount}` : ''}
                          {c.observed_at ? ` · on ${c.observed_at.slice(0, 10)}` : ''}
                          {c.as_of ? ` · data as of ${c.as_of.slice(0, 16).replace('T', ' ')} UTC` : ''}
                        </p>
                      </li>
                    ))}
                  </ul>
                </section>
              );
            })}
          </div>

          <div className="mt-4 flex flex-wrap gap-2 text-[11px] text-zinc-500">
            {result.sources.map(s => (
              <span key={s.id} className="rounded-md border border-zinc-800 px-2 py-0.5" title={s.hosts?.join(', ')}>
                {SOURCE_LABELS[s.id] ?? s.id} · {sourceState(s)}{s.sends_address ? ' · address sent' : ''}
              </span>
            ))}
          </div>

          <div className="mt-5">
            <h5 className="text-xs font-semibold text-zinc-300">Free third-party tools</h5>
            <p className="text-[11px] text-zinc-600">Opens in a new tab and sends this address to that site.</p>
            <ul className="mt-2 grid sm:grid-cols-2 gap-2">
              {RISK_TOOLS.map(t => (
                <li key={t.id}>
                  <a href={t.url(result.address)} target="_blank" rel="noopener noreferrer"
                     className="text-xs text-zinc-300 hover:text-white inline-flex items-center gap-1">
                    {t.label} <ExternalLink size={11} />
                  </a>
                  <span className="ml-2 text-[11px] text-zinc-600">{t.hint}</span>
                </li>
              ))}
            </ul>
            <p className="mt-3 text-[11px] text-zinc-500">
              Check your own wallet's token approvals →{' '}
              <a href={REVOKE_CASH_URL} target="_blank" rel="noopener noreferrer" className="text-zinc-300 hover:text-white">
                Revoke.cash
              </a>
            </p>
          </div>
        </Panel>
      )}

      <AddressRiskOverview sources={meta} onInspect={inspect} />
    </div>
  );
}

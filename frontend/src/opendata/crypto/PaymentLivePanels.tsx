import { useEffect, useState } from 'react';
import { ExternalLink } from 'lucide-react';
import type {
  AddressRiskChain,
  AddressRiskLookup,
  AddressRiskSource,
  PaymentAlert,
  PaymentBalanceRow,
  PaymentLiveResponse,
  PaymentNetwork,
  PaymentTx,
} from '../../types';
import { fetchAddressRiskLookup } from '../../api';
import { Panel } from './shared';
import RiskResultView from './RiskResultView';
import { RISK_BADGES, sourceState } from './addressRiskTools';
import {
  FLAG_META,
  flagBadgeClass,
  fmtAsOf,
  fmtDurationSec,
  formatPayError,
  formatTokenAmount,
  levelColors,
  levelCopy,
  PAY_NETWORKS,
  PAY_SOURCE_LABELS,
  recentTransferSourceId,
  shortAddr,
  tierBadgeInfo,
} from './paymentCheck';

export function PaymentAlertsBanner({
  alerts,
  network,
}: {
  alerts: PaymentAlert[];
  network: PaymentNetwork;
}) {
  if (!alerts || alerts.length === 0) return null;
  const netOpt = PAY_NETWORKS[network];
  return (
    <div className="space-y-2" role="region" aria-label="Payment security alerts">
      {alerts.map((a, idx) => {
        const isCritical = a.severity === 'critical';
        const txUrl = a.tx_hash
          ? network === 'tron'
            ? `https://tronscan.org/#/transaction/${a.tx_hash}`
            : network === 'sol'
              ? `https://solscan.io/tx/${a.tx_hash}`
              : `${netOpt.addrExplorer('').replace('/address/', '/tx/')}${a.tx_hash}`
          : '';
        return (
          <div
            key={`${a.code}-${a.tx_hash ?? idx}`}
            className={`rounded-xl border px-4 py-3 text-xs flex flex-wrap items-center justify-between gap-2 ${
              isCritical
                ? 'border-red-500/40 bg-red-500/15 text-red-100'
                : 'border-orange-500/40 bg-orange-500/15 text-orange-100'
            }`}
          >
            <div className="flex flex-wrap items-center gap-2">
              <span
                className={`rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide border ${
                  isCritical
                    ? 'border-red-400/40 bg-red-500/20 text-red-200'
                    : 'border-orange-400/40 bg-orange-500/20 text-orange-200'
                }`}
              >
                {isCritical ? 'Critical' : 'Warning'}
              </span>
              <span>{a.message}</span>
            </div>
            {a.tx_hash && (
              <a
                href={txUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="font-mono text-[11px] underline underline-offset-2 inline-flex items-center gap-1 opacity-90 hover:opacity-100"
              >
                tx {shortAddr(a.tx_hash)} <ExternalLink size={11} />
              </a>
            )}
          </div>
        );
      })}
    </div>
  );
}

export function PaymentBalancePanel({ live }: { live: PaymentLiveResponse }) {
  const isTron = live.network === 'tron';
  const netOpt = PAY_NETWORKS[live.network];
  const headsNote =
    live.heads.latest > 0
      ? isTron
        ? `solidified #${live.heads.finalized.toLocaleString('en')} · latest #${live.heads.latest.toLocaleString('en')}`
        : live.network === 'btc'
          ? `finalized (≥6 conf) #${live.heads.finalized.toLocaleString('en')} · safe (3 conf) #${live.heads.safe.toLocaleString('en')} · latest #${live.heads.latest.toLocaleString('en')}`
          : live.network === 'sol'
            ? `finalized slot #${live.heads.finalized.toLocaleString('en')} · confirmed slot #${live.heads.safe.toLocaleString('en')} · processed slot #${live.heads.latest.toLocaleString('en')}`
            : `finalized #${live.heads.finalized.toLocaleString('en')} · safe #${live.heads.safe.toLocaleString('en')} · latest #${live.heads.latest.toLocaleString('en')}`
      : `updated ${fmtAsOf(live.as_of)}`;

  return (
    <Panel title={`Balance · ${live.asset} on ${live.network_label}`} note={headsNote}>
      {live.balance_error ? (
        <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
          Balance unavailable ({formatPayError(live.balance_error)})
        </div>
      ) : live.balance.length === 0 ? (
        <div className="text-xs text-zinc-500">No balance rows available.</div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs font-mono">
            <thead>
              <tr className="border-b border-zinc-800/70 text-left text-[11px] text-zinc-500 font-sans">
                <th className="py-1.5 pr-3 font-medium">Token / Contract</th>
                <th className="py-1.5 px-3 font-medium">
                  {isTron ? 'Finalized (solidified)' : 'Finalized'}
                </th>
                <th className="py-1.5 px-3 font-medium">Safe, not final</th>
                <th className="py-1.5 pl-3 font-medium">Latest only</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-800/40">
              {live.balance.map((row: PaymentBalanceRow) => {
                const tierInfo = tierBadgeInfo(row.tier);
                return (
                  <tr key={`${row.label}-${row.contract}`}>
                    <td className="py-2 pr-3 font-sans">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="font-mono font-semibold text-zinc-100">{row.label}</span>
                        <span
                          className={`rounded border px-1.5 py-0.5 text-[10px] font-medium ${tierInfo.className}`}
                        >
                          {tierInfo.label}
                        </span>
                        {row.contract && (
                          <a
                            href={netOpt.addrExplorer(row.contract)}
                            target="_blank"
                            rel="noopener noreferrer"
                            className="font-mono text-[11px] text-zinc-500 hover:text-zinc-300 inline-flex items-center gap-0.5"
                            title={row.contract}
                          >
                            {shortAddr(row.contract)} <ExternalLink size={10} />
                          </a>
                        )}
                      </div>
                      {row.tier === 'bridged' && (
                        <div className="mt-0.5 text-[11px] text-amber-300/90">
                          ⚠ {tierInfo.detail}
                        </div>
                      )}
                    </td>
                    {row.error ? (
                      <td colSpan={3} className="py-2 px-3 font-sans text-amber-300">
                        Balance unavailable ({formatPayError(row.error)})
                      </td>
                    ) : (
                      <>
                        <td className="py-2 px-3 text-sm text-zinc-100">
                          {formatTokenAmount(row.finalized)}
                        </td>
                        <td className="py-2 px-3 text-zinc-300">
                          {isTron ? (
                            <span className="text-zinc-600" title="TRON uses 2-tier solidity finality">
                              —
                            </span>
                          ) : (
                            formatTokenAmount(row.safe_delta)
                          )}
                        </td>
                        <td className="py-2 pl-3 text-zinc-300">
                          {formatTokenAmount(row.latest_delta)}
                        </td>
                      </>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}

export function PaymentLatestCard({
  live,
  pollSec,
  onInspect,
}: {
  live: PaymentLiveResponse;
  pollSec: number;
  onInspect?: (address: string, chain: AddressRiskChain) => void;
}) {
  const tx = live.latest;
  const netOpt = PAY_NETWORKS[live.network];
  const note = `updated ${fmtAsOf(live.as_of)} · refreshes every ${pollSec}s`;
  const recentSrcId = recentTransferSourceId(live.network, live.asset);
  const recentSrc = live.sources.find(s => s.id === recentSrcId);
  const recentErrReason = recentSrc?.status === 'error' ? formatPayError(recentSrc.error) : '';

  return (
    <Panel title={`Latest incoming · ${live.asset}`} note={note}>
      {!tx ? (
        recentErrReason ? (
          <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
            Couldn&apos;t check recent transfers ({recentErrReason})
          </div>
        ) : (
          <div className="text-xs text-zinc-400">
            No genuine non-dust incoming {live.asset} transfer found in recent blocks or cached 7-day history.
          </div>
        )
      ) : (
        <div className="space-y-4">
          {recentErrReason && (
            <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
              Couldn&apos;t check recent transfers ({recentErrReason})
            </div>
          )}
          <LatestSettlementBody tx={tx} live={live} netOpt={netOpt} onInspect={onInspect} />
          <PayerCleanlinessSection
            key={`${live.network}:${tx.tx_hash}:${tx.counterparty}`}
            payer={tx.counterparty}
            network={live.network}
            onInspect={onInspect}
          />
        </div>
      )}
    </Panel>
  );
}

function LatestSettlementBody({
  tx,
  live,
  netOpt,
  onInspect,
}: {
  tx: PaymentTx;
  live: PaymentLiveResponse;
  netOpt: (typeof PAY_NETWORKS)[PaymentNetwork];
  onInspect?: (address: string, chain: AddressRiskChain) => void;
}) {
  const colors = levelColors(tx.level);
  const tierInfo = tierBadgeInfo(tx.token_tier);
  const confirmations =
    live.network === 'sol'
      ? null
      : tx.block > 0
        ? live.heads.latest >= tx.block
          ? live.heads.latest - tx.block + 1
          : 1
        : live.network === 'btc'
          ? 0
          : null;

  return (
    <div className="space-y-3">
      {/* Level badge + progress bar */}
      <div className="space-y-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <span
              className={`rounded-md border px-2 py-0.5 text-xs font-mono font-semibold ${colors.bg} ${colors.text} ${colors.border}`}
            >
              {tx.level} · {tx.progress}%
            </span>
            {confirmations !== null && (
              <span className="font-mono text-xs text-zinc-400">
                {confirmations === 0
                  ? '0 confirmations (unconfirmed in mempool)'
                  : `${confirmations.toLocaleString('en')} confirmation${confirmations === 1 ? '' : 's'}`}
              </span>
            )}
            {tx.level !== 'FINALIZED' && tx.est_sec_left > 0 && (
              <span className="font-mono text-xs text-zinc-400">
                ~{fmtDurationSec(tx.est_sec_left)} remaining
              </span>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-3 text-xs font-mono text-zinc-400">
            {tx.block > 0 ? (
              <a
                href={netOpt.blockExplorer(tx.block)}
                target="_blank"
                rel="noopener noreferrer"
                className="hover:text-zinc-200 inline-flex items-center gap-1"
              >
                {live.network === 'sol' ? 'slot' : 'block'} #{tx.block.toLocaleString('en')} <ExternalLink size={11} />
              </a>
            ) : (
              <span>{live.network === 'btc' ? 'unconfirmed (mempool)' : 'block pending'}</span>
            )}
            <a
              href={tx.explorer_url}
              target="_blank"
              rel="noopener noreferrer"
              className="hover:text-zinc-200 inline-flex items-center gap-1"
            >
              tx {shortAddr(tx.tx_hash)} <ExternalLink size={11} />
            </a>
          </div>
        </div>

        <div className="h-2 w-full rounded-full bg-zinc-800 overflow-hidden">
          <div
            className="h-full transition-all duration-300"
            style={{ width: `${Math.max(2, tx.progress)}%`, background: colors.bar }}
          />
        </div>

        <p className="text-xs text-zinc-300">
          {levelCopy(tx.level, tx.est_sec_left, live.network, tx.block)}
        </p>
      </div>

      {/* Amount, token tier, payer, and flags */}
      <div className="rounded-xl border border-zinc-800/70 bg-zinc-900/40 p-3 space-y-2 text-xs">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-base font-mono font-semibold text-white">
              +{formatTokenAmount(tx.amount)} {tx.symbol}
            </span>
            <span className={`rounded border px-1.5 py-0.5 text-[10px] font-medium ${tierInfo.className}`}>
              {tierInfo.label}
            </span>
            {tx.flags.map(f => {
              const meta = FLAG_META[f];
              const sev = meta?.severity ?? 'info';
              return (
                <span
                  key={f}
                  title={meta?.description}
                  className={`rounded border px-1.5 py-0.5 text-[10px] font-medium ${flagBadgeClass(sev)}`}
                >
                  {meta?.label ?? f}
                </span>
              );
            })}
            {tx.counterparty_hits?.map(src => {
              const b = RISK_BADGES[src];
              return (
                <span
                  key={src}
                  className="rounded border border-red-500/40 bg-red-500/15 px-1.5 py-0.5 text-[10px] font-semibold text-red-300"
                >
                  {b?.label ?? src}
                </span>
              );
            })}
          </div>
          {tx.timestamp && (
            <span className="font-mono text-[11px] text-zinc-500">
              {tx.timestamp.replace('T', ' ').replace('Z', ' UTC')}
            </span>
          )}
        </div>

        {tx.token_tier === 'bridged' && (
          <p className="text-[11px] text-amber-300">⚠ {tierInfo.detail}</p>
        )}

        <div className="flex flex-wrap items-center justify-between gap-2 pt-1 border-t border-zinc-800/60">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-zinc-500">From payer:</span>
            <a
              href={netOpt.addrExplorer(tx.counterparty)}
              target="_blank"
              rel="noopener noreferrer"
              className="font-mono text-zinc-200 hover:text-white inline-flex items-center gap-1 break-all"
            >
              {tx.counterparty} <ExternalLink size={11} />
            </a>
          </div>
          {onInspect && (
            <button
              type="button"
              onClick={() => onInspect(tx.counterparty, live.network)}
              className="rounded-lg border border-zinc-700 bg-zinc-800/80 px-2.5 py-1 text-[11px] font-medium text-zinc-200 hover:bg-zinc-700 hover:text-white"
            >
              Open in Address Risk
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

// Independent payer cleanliness check (spec §5.7, Task 12).
// Keyed by `${network}:${tx_hash}:${payer}` in the parent so each new incoming
// transfer mounts a fresh instance without resetting state synchronously inside
// an effect.
function PayerCleanlinessSection({
  payer,
  network,
  onInspect,
}: {
  payer: string;
  network: PaymentNetwork;
  onInspect?: (address: string, chain: AddressRiskChain) => void;
}) {
  const [result, setResult] = useState<AddressRiskLookup | null>(null);
  const [error, setError] = useState<string>('');
  const [loading, setLoading] = useState<boolean>(true);

  useEffect(() => {
    const ctrl = new AbortController();
    fetchAddressRiskLookup(payer, network, ctrl.signal)
      .then(r => {
        if (ctrl.signal.aborted) return;
        setResult(r);
        setError('');
      })
      .catch(e => {
        if (ctrl.signal.aborted) return;
        const d = e.response?.data;
        setError(typeof d === 'string' && d ? d : e.message || 'lookup failed');
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });
    return () => ctrl.abort();
  }, [payer, network]);

  const sentHosts = result
    ? Array.from(
        new Set(
          result.sources
            .filter(s => s.sends_address && s.hosts && s.hosts.length > 0)
            .flatMap(s => s.hosts ?? []),
        ),
      )
    : [];

  return (
    <div className="rounded-xl border border-zinc-800/80 bg-zinc-950/60 p-3.5 space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs font-semibold text-zinc-300">
          Payer screening (independent of settlement)
        </span>
        {onInspect && (
          <button
            type="button"
            onClick={() => onInspect(payer, network)}
            className="text-[11px] text-cyan-400 hover:text-cyan-300"
          >
            Full report in Address Risk →
          </button>
        )}
      </div>

      <p className="text-[11px] text-zinc-500">
        {sentHosts.length > 0
          ? `Payer screening sends the payer's address to: ${sentHosts.join(', ')}.`
          : `Payer screening sends the payer's address to configured ${PAY_NETWORKS[network].label} risk sources (GoPlus, RPCs, Blockscout).`}
      </p>

      {loading ? (
        <div className="text-xs text-zinc-500">Screening payer address {shortAddr(payer)}…</div>
      ) : error ? (
        <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
          Payer screening unavailable ({error}). Settlement status above is unaffected.
        </div>
      ) : result ? (
        <RiskResultView result={result} compact />
      ) : null}
    </div>
  );
}

export function PaymentSourcesFooter({
  liveSources,
  historySources,
}: {
  liveSources: AddressRiskSource[];
  historySources?: AddressRiskSource[];
}) {
  const mergedMap = new Map<string, AddressRiskSource>();
  for (const s of liveSources) {
    mergedMap.set(s.id, s);
  }
  if (historySources) {
    for (const s of historySources) {
      const existing = mergedMap.get(s.id);
      if (!existing || (existing.status === 'ok' && s.status !== 'ok')) {
        mergedMap.set(s.id, s);
      }
    }
  }
  const merged = Array.from(mergedMap.values());
  const allHosts = Array.from(new Set(merged.flatMap(s => s.hosts ?? [])));

  return (
    <div className="rounded-xl border border-zinc-800/60 bg-zinc-900/30 px-4 py-3 space-y-2 text-[11px] text-zinc-500">
      <div className="flex flex-wrap gap-2">
        {merged.map(s => (
          <span
            key={s.id}
            className="rounded-md border border-zinc-800 px-2 py-0.5"
            title={s.hosts?.join(', ')}
          >
            {PAY_SOURCE_LABELS[s.id] ?? s.id} · {sourceState(s)}
          </span>
        ))}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span>
          {allHosts.length > 0
            ? `Upstream hosts queried for this address: ${allHosts.join(', ')}`
            : 'Local checks run on this server.'}
        </span>
        <span className="text-zinc-400">Informational only; not a guarantee of payment.</span>
      </div>
    </div>
  );
}

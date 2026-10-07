import { Fragment, useState } from 'react';
import { ChevronDown, ChevronRight, ExternalLink } from 'lucide-react';
import type {
  AddressRiskChain,
  PaymentHeads,
  PaymentHistoryResponse,
  PaymentTx,
} from '../../types';
import { ErrorBanner } from '../../dashboards/shared';
import { Panel } from './shared';
import { RISK_BADGES } from './addressRiskTools';
import {
  flagBadgeClass,
  fmtAsOf,
  formatPayError,
  formatTokenAmount,
  historySourceId,
  levelColors,
  PAY_NETWORKS,
  primaryFlagForTx,
  shortAddr,
  tierBadgeInfo,
} from './paymentCheck';
import PaymentTxDetail from './PaymentTxDetail';

type DirFilter = 'all' | 'in' | 'out';

// Returns true when a row should be hidden by the "Hide zero-value & unrelated tokens"
// toggle (spec Task 13: hides `other` tier, standalone `dust`, and standalone `zero_value`,
// but NEVER hides poisoning evidence like `zero_value + lookalike` or `dust + lookalike`).
function shouldHideNoiseRow(tx: PaymentTx): boolean {
  const hasCriticalOrPoisonFlag =
    tx.flags.includes('lookalike') ||
    tx.flags.includes('sent_to_lookalike') ||
    tx.flags.includes('counterfeit_token') ||
    tx.flags.includes('counterparty_listed') ||
    tx.flags.includes('failed');
  if (hasCriticalOrPoisonFlag) {
    return false;
  }
  if (tx.token_tier === 'other') {
    return true;
  }
  if (tx.flags.includes('zero_value') || tx.flags.includes('dust')) {
    return true;
  }
  return false;
}

export default function PaymentHistory({
  history,
  loading,
  error,
  heads,
  onInspect,
  onRetry,
}: {
  history: PaymentHistoryResponse | null;
  loading: boolean;
  error: string;
  heads?: PaymentHeads;
  onInspect?: (address: string, chain: AddressRiskChain) => void;
  onRetry?: () => void;
}) {
  const [dirFilter, setDirFilter] = useState<DirFilter>('all');
  const [hideNoise, setHideNoise] = useState<boolean>(true);
  const [expandedKey, setExpandedKey] = useState<string | null>(null);

  if (error && !history) {
    return (
      <Panel title="7-day History" note="transfers in and out">
        <div className="space-y-2">
          <ErrorBanner message={error} />
          {onRetry && (
            <button
              type="button"
              onClick={onRetry}
              disabled={loading}
              className="rounded border border-zinc-700 bg-zinc-800 px-2.5 py-1 text-xs font-medium text-zinc-200 hover:bg-zinc-700 disabled:opacity-50"
            >
              {loading ? 'Retrying…' : 'Retry'}
            </button>
          )}
        </div>
      </Panel>
    );
  }

  if (loading && !history) {
    return (
      <Panel title="7-day History" note="loading…">
        <div className="text-xs text-zinc-500">Loading 7-day transaction history…</div>
      </Panel>
    );
  }

  if (!history) return null;

  const netOpt = PAY_NETWORKS[history.network];
  const histSrc = history.sources.find(s => s.id === historySourceId(history.network));
  const histErrReason = histSrc?.status === 'error' ? formatPayError(histSrc.error) : '';
  const historyUnavailableEmpty = Boolean(histErrReason) && history.txs.length === 0;

  const filtered = history.txs.filter(tx => {
    if (dirFilter !== 'all' && tx.direction !== dirFilter) return false;
    if (hideNoise && shouldHideNoiseRow(tx)) return false;
    return true;
  });
  const hiddenCount = history.txs.length - filtered.length;

  const scopeParts: string[] = [];
  if (history.scope.tokentx > 0) scopeParts.push(`tokentx: ${history.scope.tokentx}`);
  if (history.scope.txlist > 0) scopeParts.push(`txlist: ${history.scope.txlist}`);
  if (history.scope.txlistinternal > 0) scopeParts.push(`internal: ${history.scope.txlistinternal}`);
  if (history.scope.trc20 > 0) scopeParts.push(`trc20: ${history.scope.trc20}`);

  const note = historyUnavailableEmpty
    ? `since ${history.since.slice(0, 10)} · history unavailable · updated ${fmtAsOf(history.as_of)}`
    : `since ${history.since.slice(0, 10)} · ${filtered.length} shown of ${history.txs.length} · updated ${fmtAsOf(history.as_of)}`;

  return (
    <Panel title={`History (7 days) · ${history.asset} on ${netOpt.label}`} note={note}>
      <div className="space-y-3">
        {histErrReason && (
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
            <span>History unavailable ({histErrReason}) — retrying</span>
            {onRetry && (
              <button
                type="button"
                onClick={onRetry}
                disabled={loading}
                className="rounded border border-amber-400/40 bg-amber-500/20 px-2.5 py-0.5 text-[11px] font-medium text-amber-100 hover:bg-amber-500/30 disabled:opacity-50"
              >
                {loading ? 'Retrying…' : 'Retry'}
              </button>
            )}
          </div>
        )}

        {!historyUnavailableEmpty && (
          <>
            {/* Filter controls */}
            <div className="flex flex-wrap items-center justify-between gap-3 text-xs">
              <div className="flex items-center gap-1" role="group" aria-label="Direction filter">
                {(['all', 'in', 'out'] as const).map(d => (
                  <button
                    key={d}
                    type="button"
                    onClick={() => setDirFilter(d)}
                    className={`rounded-lg px-2.5 py-1 text-xs font-medium capitalize transition-colors ${
                      dirFilter === d
                        ? 'bg-zinc-800 text-white'
                        : 'text-zinc-500 hover:text-zinc-300'
                    }`}
                  >
                    {d === 'all' ? 'All' : d === 'in' ? 'In' : 'Out'}
                  </button>
                ))}
              </div>

              <label className="inline-flex items-center gap-2 text-xs text-zinc-400 cursor-pointer select-none">
                <input
                  type="checkbox"
                  checked={hideNoise}
                  onChange={e => setHideNoise(e.target.checked)}
                  className="rounded border-zinc-700 bg-zinc-900 text-cyan-500 focus:ring-0"
                />
                <span>
                  Hide zero-value &amp; unrelated tokens
                  {hideNoise && hiddenCount > 0 ? ` (${hiddenCount} hidden)` : ''}
                </span>
              </label>
            </div>

            {/* Table */}
            {filtered.length === 0 ? (
              <div className="py-6 text-center text-xs text-zinc-500">
                No transfers match the current filters in the last 7 days.
              </div>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-xs">
                  <thead>
                    <tr className="border-b border-zinc-800/70 text-left text-[11px] text-zinc-500">
                      <th className="py-2 pr-3 font-medium">Time</th>
                      <th className="py-2 px-2 font-medium">Dir</th>
                      <th className="py-2 px-3 font-medium">Amount</th>
                      <th className="py-2 px-3 font-medium">Counterparty</th>
                      <th className="py-2 px-3 font-medium">Block · Tx ↗</th>
                      <th className="py-2 pl-3 font-medium">Status</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-zinc-800/40 font-mono">
                    {filtered.map((tx, idx) => {
                      const rowKey = `${tx.tx_hash}:${tx.token_contract}:${tx.direction}:${tx.counterparty}:${idx}`;
                      const isOpen = expandedKey === rowKey;
                      const colors = levelColors(tx.level);
                      const tierInfo = tierBadgeInfo(tx.token_tier);
                      const topFlag = primaryFlagForTx(tx.flags);
                      const isCounterfeit = tx.token_tier === 'counterfeit';

                      return (
                        <Fragment key={rowKey}>
                          <tr
                            onClick={() => setExpandedKey(isOpen ? null : rowKey)}
                            className="cursor-pointer hover:bg-zinc-900/50 transition-colors"
                          >
                            <td className="py-2 pr-3 whitespace-nowrap text-zinc-400">
                              <span className="inline-flex items-center gap-1">
                                {isOpen ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                                {tx.timestamp
                                  ? tx.timestamp.slice(5, 19).replace('T', ' ')
                                  : 'pending'}
                              </span>
                            </td>
                            <td className="py-2 px-2 whitespace-nowrap">
                              <span
                                className={`rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase ${
                                  tx.direction === 'in'
                                    ? 'bg-emerald-500/15 text-emerald-300'
                                    : 'bg-zinc-800 text-zinc-300'
                                }`}
                              >
                                {tx.direction}
                              </span>
                            </td>
                            <td className="py-2 px-3 whitespace-nowrap">
                              <span
                                className={
                                  isCounterfeit
                                    ? 'line-through text-red-400'
                                    : tx.direction === 'in'
                                      ? 'text-zinc-100'
                                      : 'text-zinc-300'
                                }
                              >
                                {tx.direction === 'in' ? '+' : '-'}
                                {formatTokenAmount(tx.amount)} {tx.symbol}
                              </span>
                              {tx.token_tier !== 'native' && (
                                <span
                                  className={`ml-1.5 rounded border px-1 py-0.5 font-sans text-[10px] font-medium ${tierInfo.className}`}
                                >
                                  {tierInfo.label}
                                </span>
                              )}
                            </td>
                            <td
                              className="py-2 px-3 whitespace-nowrap text-zinc-300"
                              title={tx.counterparty}
                            >
                              <span>{shortAddr(tx.counterparty)}</span>
                              {tx.counterparty_hits?.map(src => {
                                const b = RISK_BADGES[src];
                                return (
                                  <span
                                    key={src}
                                    className="ml-1.5 rounded border border-red-500/40 bg-red-500/15 px-1 py-0.5 font-sans text-[10px] font-semibold text-red-300"
                                  >
                                    {b?.label ?? src}
                                  </span>
                                );
                              })}
                            </td>
                            <td className="py-2 px-3 whitespace-nowrap text-zinc-400">
                              <span className="mr-2">
                                {tx.block > 0 ? `#${tx.block.toLocaleString('en')}` : 'pending'}
                              </span>
                              <a
                                href={tx.explorer_url}
                                target="_blank"
                                rel="noopener noreferrer"
                                onClick={e => e.stopPropagation()}
                                className="text-zinc-400 hover:text-zinc-200 inline-flex items-center gap-0.5"
                                title={tx.tx_hash}
                              >
                                {shortAddr(tx.tx_hash)} <ExternalLink size={11} />
                              </a>
                            </td>
                            <td className="py-2 pl-3 whitespace-nowrap font-sans">
                              <div className="flex flex-wrap items-center gap-1.5">
                                <span
                                  className={`rounded border px-1.5 py-0.5 font-mono text-[10px] font-semibold ${colors.bg} ${colors.text} ${colors.border}`}
                                >
                                  {tx.level === 'FINALIZED' ? 'FINALIZED' : `${tx.level} ${tx.progress}%`}
                                </span>
                                {topFlag && (
                                  <span
                                    className={`rounded border px-1.5 py-0.5 text-[10px] font-medium ${flagBadgeClass(topFlag.severity)}`}
                                  >
                                    {topFlag.label}
                                  </span>
                                )}
                              </div>
                            </td>
                          </tr>
                          {isOpen && (
                            <tr>
                              <td colSpan={6} className="py-3 px-2 font-sans bg-zinc-950/50">
                                <PaymentTxDetail
                                  tx={tx}
                                  asset={history.asset}
                                  network={history.network}
                                  heads={heads}
                                  onInspect={onInspect}
                                />
                              </td>
                            </tr>
                          )}
                        </Fragment>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}

        {/* Scope footer */}
        <div className="pt-2 border-t border-zinc-800/60 flex flex-wrap items-center justify-between gap-2 text-[11px] text-zinc-500">
          <span>
            Source: {history.scope.hosts?.join(', ') || netOpt.label}
            {histErrReason ? ` · ${histErrReason}` : ''}
            {scopeParts.length > 0 ? ` · ${scopeParts.join(' · ')}` : ''}
          </span>
          {history.scope.truncated && (
            <span className="text-amber-300">
              Showing the newest 1000 transfers; older ones in the 7-day window are not shown.
            </span>
          )}
        </div>
      </div>
    </Panel>
  );
}

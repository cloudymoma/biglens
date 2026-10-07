import { useState } from 'react';
import { ExternalLink } from 'lucide-react';
import type {
  AddressRiskChain,
  AddressRiskLookup,
  PaymentAsset,
  PaymentHeads,
  PaymentNetwork,
  PaymentTx,
} from '../../types';
import { fetchAddressRiskLookup } from '../../api';
import RiskResultView from './RiskResultView';
import { RISK_BADGES, RISK_SOURCE_LABELS } from './addressRiskTools';
import {
  FLAG_META,
  flagBadgeClass,
  levelColors,
  levelCopy,
  OFFICIAL_CONTRACTS,
  PAY_NETWORKS,
  tierBadgeInfo,
} from './paymentCheck';

export default function PaymentTxDetail({
  tx,
  asset,
  network,
  heads,
  onInspect,
}: {
  tx: PaymentTx;
  asset: PaymentAsset;
  network: PaymentNetwork;
  heads?: PaymentHeads;
  onInspect?: (address: string, chain: AddressRiskChain) => void;
}) {
  const [riskResult, setRiskResult] = useState<AddressRiskLookup | null>(null);
  const [riskLoading, setRiskLoading] = useState<boolean>(false);
  const [riskError, setRiskError] = useState<string>('');

  const netOpt = PAY_NETWORKS[network];
  const colors = levelColors(tx.level);
  const tierInfo = tierBadgeInfo(tx.token_tier);
  const officialList = OFFICIAL_CONTRACTS[`${asset}:${network}`] ?? [];

  const runFullCheck = () => {
    if (!tx.counterparty || riskLoading) return;
    setRiskLoading(true);
    setRiskError('');
    fetchAddressRiskLookup(tx.counterparty, network)
      .then(r => {
        setRiskResult(r);
      })
      .catch(e => {
        const d = e.response?.data;
        setRiskError(typeof d === 'string' && d ? d : e.message || 'lookup failed');
      })
      .finally(() => {
        setRiskLoading(false);
      });
  };

  const headRelation = (() => {
    const blockLabel = tx.block > 0 ? `block #${tx.block.toLocaleString('en')}` : 'block pending';
    if (!heads || heads.latest === 0) return blockLabel;
    if (network === 'tron') {
      return `${blockLabel} · solidified at #${heads.finalized.toLocaleString('en')} · latest #${heads.latest.toLocaleString('en')}`;
    }
    return `${blockLabel} · finalized at #${heads.finalized.toLocaleString('en')} · safe at #${heads.safe.toLocaleString('en')} · latest #${heads.latest.toLocaleString('en')}`;
  })();

  return (
    <div className="rounded-xl border border-zinc-800 bg-zinc-950/90 p-4 space-y-4 text-xs">
      <div className="grid gap-4 md:grid-cols-2">
        {/* 1. Settlement */}
        <div className="rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-3 space-y-2">
          <div className="flex items-center justify-between gap-2">
            <span className="font-semibold text-zinc-200">Settlement</span>
            <span
              className={`rounded border px-2 py-0.5 font-mono text-[11px] font-semibold ${colors.bg} ${colors.text} ${colors.border}`}
            >
              {tx.level} · {tx.progress}%
            </span>
          </div>
          <div className="h-1.5 w-full rounded-full bg-zinc-800 overflow-hidden">
            <div
              className="h-full"
              style={{ width: `${Math.max(2, tx.progress)}%`, background: colors.bar }}
            />
          </div>
          <p className="font-mono text-[11px] text-zinc-400">{headRelation}</p>
          <p className="text-zinc-300">{levelCopy(tx.level, tx.est_sec_left)}</p>
          {tx.failed && (
            <p className="text-red-300 font-medium">
              Transaction failed / reverted on-chain — no funds were transferred.
            </p>
          )}
        </div>

        {/* 2. Token */}
        <div className="rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-3 space-y-2">
          <div className="flex items-center justify-between gap-2">
            <span className="font-semibold text-zinc-200">Token</span>
            <span className={`rounded border px-1.5 py-0.5 text-[10px] font-medium ${tierInfo.className}`}>
              {tierInfo.label}
            </span>
          </div>
          <p className="text-zinc-300">{tierInfo.detail}</p>
          {tx.token_contract ? (
            <div className="font-mono text-[11px] text-zinc-400 break-all">
              Contract:{' '}
              <a
                href={netOpt.addrExplorer(tx.token_contract)}
                target="_blank"
                rel="noopener noreferrer"
                className="text-zinc-200 hover:text-white inline-flex items-center gap-1"
              >
                {tx.token_contract} <ExternalLink size={11} />
              </a>
            </div>
          ) : (
            <div className="font-mono text-[11px] text-zinc-500">Native chain asset ({tx.symbol})</div>
          )}
          {tx.token_tier === 'counterfeit' && officialList.length > 0 && (
            <div className="rounded border border-red-500/30 bg-red-500/10 p-2 text-[11px] text-red-200 space-y-1">
              <div className="font-semibold">
                Official {asset} contract{officialList.length > 1 ? 's' : ''} on {netOpt.label}:
              </div>
              {officialList.map(o => (
                <div key={o.contract} className="font-mono break-all">
                  {o.label}: {o.contract}
                </div>
              ))}
            </div>
          )}
        </div>
      </div>

      {/* 3. Flags */}
      <div className="rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-3 space-y-2">
        <div className="font-semibold text-zinc-200">Flags</div>
        {tx.flags.length === 0 ? (
          <p className="text-zinc-500">No local risk flags triggered on this transfer.</p>
        ) : (
          <ul className="space-y-1.5">
            {tx.flags.map(f => {
              const meta = FLAG_META[f];
              const sev = meta?.severity ?? 'info';
              return (
                <li key={f} className="flex flex-wrap items-start gap-2">
                  <span
                    className={`rounded border px-1.5 py-0.5 text-[10px] font-medium shrink-0 ${flagBadgeClass(sev)}`}
                  >
                    {meta?.label ?? f}
                  </span>
                  <span className="text-zinc-300">{meta?.description ?? f}</span>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      {/* 4. Counterparty */}
      <div className="rounded-lg border border-zinc-800/80 bg-zinc-900/40 p-3 space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="space-y-1">
            <div className="font-semibold text-zinc-200">
              Counterparty ({tx.direction === 'in' ? 'sender' : 'recipient'})
            </div>
            {tx.counterparty ? (
              <a
                href={netOpt.addrExplorer(tx.counterparty)}
                target="_blank"
                rel="noopener noreferrer"
                className="font-mono text-zinc-200 hover:text-white inline-flex items-center gap-1 break-all"
              >
                {tx.counterparty} <ExternalLink size={11} />
              </a>
            ) : (
              <span className="text-zinc-500">Unknown counterparty</span>
            )}
          </div>
          {tx.counterparty && (
            <div className="flex flex-wrap items-center gap-2">
              <button
                type="button"
                onClick={runFullCheck}
                disabled={riskLoading}
                className="rounded-lg bg-cyan-600 px-2.5 py-1 text-[11px] font-medium text-white hover:bg-cyan-500 disabled:opacity-50"
              >
                {riskLoading ? 'Checking…' : riskResult ? 'Re-run full check' : 'Run full check'}
              </button>
              {onInspect && (
                <button
                  type="button"
                  onClick={() => onInspect(tx.counterparty, network)}
                  className="rounded-lg border border-zinc-700 bg-zinc-800 px-2.5 py-1 text-[11px] font-medium text-zinc-200 hover:bg-zinc-700 hover:text-white"
                >
                  Open in Address Risk
                </button>
              )}
            </div>
          )}
        </div>

        {/* Local list hits */}
        {tx.counterparty_hits && tx.counterparty_hits.length > 0 ? (
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-red-300 font-medium">Local list hits:</span>
            {tx.counterparty_hits.map(src => {
              const b = RISK_BADGES[src];
              return (
                <span
                  key={src}
                  className="rounded border border-red-500/40 bg-red-500/15 px-1.5 py-0.5 text-[10px] font-semibold text-red-200"
                >
                  {b?.label ?? src} ({RISK_SOURCE_LABELS[src] ?? src})
                </span>
              );
            })}
          </div>
        ) : (
          <p className="text-[11px] text-zinc-500">
            No hits on local lists for this chain. Click &ldquo;Run full check&rdquo; to query live issuer freeze, GoPlus, and oracle sources.
          </p>
        )}

        {riskError && (
          <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
            Full check failed: {riskError}
          </div>
        )}

        {riskResult && (
          <div className="rounded-lg border border-zinc-800 bg-zinc-950/80 p-3">
            <RiskResultView result={riskResult} compact />
          </div>
        )}
      </div>
    </div>
  );
}

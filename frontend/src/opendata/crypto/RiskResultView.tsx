import type React from 'react';
import { ExternalLink } from 'lucide-react';
import type {
  AddressRiskChain,
  AddressRiskLookup,
  AddressRiskScope,
  AddressRiskSeverity,
} from '../../types';
import { Panel } from './shared';
import {
  ago,
  REVOKE_CASH_URL,
  RISK_SOURCE_LABELS as SOURCE_LABELS,
  RISK_TOOLS,
  sourceState,
} from './addressRiskTools';

// Severity is always shown as text + count; color is a secondary cue only.
const GROUPS: { id: AddressRiskSeverity; label: string; color: string }[] = [
  { id: 'critical', label: 'Critical', color: '#f87171' },
  { id: 'warning', label: 'Warning', color: '#fb923c' },
  { id: 'association', label: 'Association', color: '#fde047' },
  { id: 'info', label: 'Info', color: '#a1a1aa' },
];

const ETHERSCAN_PAGE = 1000;
const SCOPE_LABELS: ['txlist' | 'tokentx' | 'txlistinternal', string][] = [
  ['txlist', 'Transactions'],
  ['tokentx', 'Token transfers'],
  ['txlistinternal', 'Internal transfers'],
];

// What the association analysis actually covered, per list (spec §8.4): for
// a busy address 1000 rows can be only a few hours.
function associationNotes(r: AddressRiskLookup): string[] {
  const es = r.sources.find(s => s.id === 'etherscan');
  if (!es || es.status === 'not_configured') {
    return ['Not checked: add a free Etherscan API key above to see transfers with listed addresses.'];
  }
  const sc: AddressRiskScope | null = r.association_scope;
  if (es.status !== 'ok' || !sc) {
    return [
      `Not checked: ${es.hosts?.includes('eth.blockscout.com') ? 'Blockscout' : 'Etherscan'} ${sourceState(es)}${es.last_error ? ` (${es.last_error})` : ''}.`,
    ];
  }
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

export default function RiskResultView({
  result,
  compact = false,
  headingRef,
}: {
  result: AddressRiskLookup;
  compact?: boolean;
  headingRef?: React.RefObject<HTMLHeadingElement | null>;
}) {
  const chain: AddressRiskChain = result.chain || 'eth';
  const esSource = result.sources.find(s => s.id === 'etherscan');
  const usedBlockscoutFallback = Boolean(esSource?.hosts?.includes('eth.blockscout.com'));
  const hasAssociationSource = Boolean(esSource) || chain === 'eth';
  const tools = RISK_TOOLS.filter(t => t.chains.includes(chain));

  const sourceChips = (
    <div className={`${compact ? 'mt-2' : 'mt-4'} flex flex-wrap gap-2 text-[11px] text-zinc-500`}>
      {result.sources.map(s => (
        <span key={s.id} className="rounded-md border border-zinc-800 px-2 py-0.5" title={s.hosts?.join(', ')}>
          {SOURCE_LABELS[s.id] ?? s.id} · {sourceState(s)}{s.sends_address ? ' · address sent' : ''}
        </span>
      ))}
    </div>
  );

  if (compact) {
    const compactGroups = GROUPS.filter(g => g.id === 'critical' || g.id === 'warning');
    return (
      <div className="space-y-2">
        <p role="status" className="text-xs text-zinc-200">{result.summary.text}</p>
        {compactGroups.map(g => {
          const clues = result.clues.filter(c => c.severity === g.id);
          if (clues.length === 0) return null;
          return (
            <section key={g.id}>
              <h5 className="text-xs font-semibold" style={{ color: g.color }}>
                {g.label} ({clues.length})
              </h5>
              <ul className="mt-1.5 space-y-1.5">
                {clues.map((c, i) => (
                  <li key={`${c.source}-${c.flag}-${i}`} className="rounded-lg border border-zinc-800 p-2.5 text-xs">
                    <div className="flex items-start justify-between gap-3">
                      <span className="text-zinc-200">
                        <span className="text-zinc-500">{g.label} · </span>{c.title}
                      </span>
                      {c.ref_url && (
                        <a
                          href={c.ref_url}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="shrink-0 text-zinc-400 hover:text-zinc-200 inline-flex items-center gap-1"
                        >
                          source <ExternalLink size={11} />
                        </a>
                      )}
                    </div>
                    {c.detail && <p className="mt-1 text-zinc-400">{c.detail}</p>}
                  </li>
                ))}
              </ul>
            </section>
          );
        })}
        {sourceChips}
      </div>
    );
  }

  return (
    <Panel title="Result" note={`checked ${ago(result.queried_at)}`}>
      <h4 ref={headingRef} tabIndex={-1} className="font-mono text-sm text-white break-all outline-none">
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
          if (clues.length === 0 && (g.id !== 'association' || !hasAssociationSource)) return null;
          return (
            <section key={g.id}>
              <h5 className="text-xs font-semibold" style={{ color: g.color }}>
                {g.label} ({clues.length})
              </h5>
              {g.id === 'association' && hasAssociationSource && (
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

      {sourceChips}

      <div className="mt-5">
        <h5 className="text-xs font-semibold text-zinc-300">Free third-party tools</h5>
        <p className="text-[11px] text-zinc-600">Opens in a new tab and sends this address to that site.</p>
        <ul className="mt-2 grid sm:grid-cols-2 gap-2">
          {tools.map(t => (
            <li key={t.id}>
              <a href={t.url(result.address, chain)} target="_blank" rel="noopener noreferrer"
                 className="text-xs text-zinc-300 hover:text-white inline-flex items-center gap-1">
                {t.labelByChain?.[chain] ?? t.label} <ExternalLink size={11} />
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
  );
}

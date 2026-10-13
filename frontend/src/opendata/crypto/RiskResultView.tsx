import type React from 'react';
import { ExternalLink } from 'lucide-react';
import type {
  AddressRiskBQPeer,
  AddressRiskBQProfile,
  AddressRiskChain,
  AddressRiskLookup,
  AddressRiskScope,
  AddressRiskSeverity,
  AddressRiskSource,
} from '../../types';
import { ChainIcon, TokenIcon } from './CryptoIcons';
import { Panel } from './shared';
import {
  ago,
  REVOKE_CASH_URL,
  RISK_BADGES,
  RISK_CHAINS,
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

// States only the BigQuery profile reports; everything else uses sourceState.
const BQ_STATE_TEXT: Record<string, string> = {
  over_budget: 'over budget (not billed)',
  rate_limited: 'hourly limit reached',
  timeout: 'timeout',
};

function chipState(s: AddressRiskSource): string {
  return BQ_STATE_TEXT[s.status] ?? sourceState(s);
}

const shortAddr = (a: string): string => (a.length > 12 ? `${a.slice(0, 6)}…${a.slice(-4)}` : a);
const fmtCount = (n: number | undefined): string => (n ?? 0).toLocaleString('en');
const fmtMiB = (bytes: number): string => `${Math.round(bytes / 2 ** 20).toLocaleString('en')} MiB`;
const fmtGiB = (bytes: number): string => `${Number((bytes / 2 ** 30).toFixed(1))} GiB`;

function fmtAge(days: number | undefined): string {
  const d = days ?? 0;
  if (d < 1) return 'less than a day';
  if (d < 60) return `${d} day${d === 1 ? '' : 's'}`;
  if (d < 730) return `${Math.floor(d / 30)} months`;
  return `${(d / 365.25).toFixed(1)} years`;
}

function ListBadges({ lists }: { lists?: string[] | null }) {
  return (
    <>
      {(lists ?? []).map(l => {
        const b = RISK_BADGES[l];
        return (
          <span
            key={l}
            className="rounded border px-1 py-px text-[10px] font-semibold"
            style={{ color: b?.color ?? '#fb923c', borderColor: `${b?.color ?? '#fb923c'}66` }}
          >
            {b?.label ?? l}
          </span>
        );
      })}
    </>
  );
}

// An address that can be opened as a new lookup (click-through), else a link.
function PeerAddress({ address, onInspect }: { address: string; onInspect?: (addr: string) => void }) {
  if (onInspect) {
    return (
      <button
        type="button"
        onClick={() => onInspect(address)}
        title={`Look up ${address}`}
        className="font-mono text-cyan-400 hover:text-cyan-300"
      >
        {shortAddr(address)}
      </button>
    );
  }
  return (
    <a
      href={`https://etherscan.io/address/${address}`}
      target="_blank"
      rel="noopener noreferrer"
      title={address}
      className="font-mono text-zinc-300 hover:text-white"
    >
      {shortAddr(address)}
    </a>
  );
}

function Stat({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-[11px] text-zinc-500">{label}</dt>
      <dd className="mt-0.5 text-zinc-200">{children}</dd>
    </div>
  );
}

function PeerLine({ p, onInspect }: { p: AddressRiskBQPeer; onInspect?: (addr: string) => void }) {
  return (
    <li className="flex flex-wrap items-center gap-1.5">
      <ListBadges lists={p.lists} />
      <PeerAddress address={p.address} onInspect={onInspect} />
      <span className="text-zinc-500">
        {fmtCount(p.sent)} sent · {fmtCount(p.received)} received
        {p.first_funder ? ' · first funder' : ''}
        {p.last_ts ? ` · last ${p.last_ts.slice(0, 10)}` : ''}
      </span>
    </li>
  );
}

// The opt-in BigQuery profile (backend/address_risk_bqprofile.go).
function BQProfileCard({ p, onInspect }: { p: AddressRiskBQProfile; onInspect?: (addr: string) => void }) {
  const f = p.first_funder;
  const listed = p.listed_peers ?? [];
  const top = p.top_peers ?? [];
  let footnote = '';
  if (p.status === 'ok') {
    footnote =
      `Billed ${fmtMiB(p.bytes_billed)} in your project` +
      (p.cached ? ' (cached result, no new charge)' : '') +
      ` · cap ${fmtGiB(p.max_bytes_billed)} per lookup · ${(p.elapsed_ms / 1000).toFixed(1)} s. ` +
      "Top-level transactions only (no internal or token transfers; failed transactions included). The address is kept in your project's BigQuery job history.";
  } else if (p.status === 'over_budget') {
    footnote = "Nothing billed. The rejected job is still listed in your project's BigQuery job history.";
  }
  return (
    <section className="mt-5 rounded-xl border border-zinc-800 bg-zinc-950/40 p-3.5" aria-label="BigQuery profile">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h5 className="text-xs font-semibold text-zinc-200">BigQuery profile</h5>
        <span className="text-[11px] text-zinc-500">full Ethereum history · Google public dataset</span>
      </div>
      {p.status !== 'ok' ? (
        <p className="mt-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
          {p.message || 'BigQuery profile unavailable.'}
        </p>
      ) : (
        <>
          <dl className="mt-3 grid gap-x-6 gap-y-2.5 text-xs sm:grid-cols-2">
            <Stat label="First funder">
              {f ? (
                <span className="inline-flex flex-wrap items-center gap-1.5">
                  <ListBadges lists={f.lists} />
                  <PeerAddress address={f.address} onInspect={onInspect} />
                  <span className="text-zinc-400">
                    {f.value_eth} ETH on {f.timestamp.slice(0, 10)} ·{' '}
                    <a
                      href={`https://etherscan.io/block/${f.block_number}`}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="hover:text-white"
                    >
                      block #{f.block_number.toLocaleString('en')}
                    </a>
                  </span>
                </span>
              ) : (
                <span className="text-zinc-400">none found (no incoming ETH transaction)</span>
              )}
            </Stat>
            <Stat label="First seen">
              {p.first_seen ? `${p.first_seen.slice(0, 10)} · account age ${fmtAge(p.account_age_days)}` : 'no transactions'}
            </Stat>
            <Stat label="Transactions">
              {fmtCount(p.outgoing?.txs)} out · {fmtCount(p.incoming?.txs)} in
            </Stat>
            <Stat label="Last active">{p.last_active ? p.last_active.slice(0, 10) : '—'}</Stat>
            <Stat label="Distinct counterparties">
              {fmtCount(p.counterparties)}{' '}
              <span className="text-zinc-500">
                ({fmtCount(p.outgoing?.peers)} sent to · {fmtCount(p.incoming?.peers)} received from)
              </span>
            </Stat>
            <Stat label="ETH moved">
              {p.outgoing?.eth ?? '0'} sent · {p.incoming?.eth ?? '0'} received
            </Stat>
          </dl>

          <div className="mt-3 text-xs">
            <h6 className="text-[11px] font-semibold text-zinc-400">Listed counterparties</h6>
            {!p.lists_checked ? (
              <p className="mt-1 text-zinc-500">Local lists are unavailable on this server; counterparties were not matched.</p>
            ) : listed.length === 0 ? (
              <p className="mt-1 text-zinc-500">
                No match on the local OFAC / MEW / freeze / lookalike lists among the {fmtCount(p.peers_analyzed)} most
                frequent counterparties.
              </p>
            ) : (
              <ul className="mt-1 space-y-1">
                {listed.map(peer => (
                  <PeerLine key={peer.address} p={peer} onInspect={onInspect} />
                ))}
              </ul>
            )}
          </div>

          {top.length > 0 && (
            <details className="mt-3 text-xs">
              <summary className="cursor-pointer text-[11px] font-semibold text-zinc-400 hover:text-zinc-200">
                Top {top.length} counterparties by transaction count
              </summary>
              <ul className="mt-1.5 space-y-1">
                {top.map(peer => (
                  <PeerLine key={peer.address} p={peer} onInspect={onInspect} />
                ))}
              </ul>
            </details>
          )}
        </>
      )}
      {footnote && <p className="mt-3 text-[11px] text-zinc-600">{footnote}</p>}
    </section>
  );
}

export default function RiskResultView({
  result,
  compact = false,
  headingRef,
  onInspect,
}: {
  result: AddressRiskLookup;
  compact?: boolean;
  headingRef?: React.RefObject<HTMLHeadingElement | null>;
  onInspect?: (address: string) => void; // opens an Ethereum lookup (BigQuery profile click-through)
}) {
  const chain: AddressRiskChain = result.chain || 'eth';
  const chainLabel = RISK_CHAINS.find(c => c.id === chain)?.label ?? chain;
  const esSource = result.sources.find(s => s.id === 'etherscan');
  const usedBlockscoutFallback = Boolean(esSource?.hosts?.includes('eth.blockscout.com'));
  const hasAssociationSource = Boolean(esSource) || chain === 'eth';
  const tools = RISK_TOOLS.filter(t => t.chains.includes(chain));

  const sourceChips = (
    <div className={`${compact ? 'mt-2' : 'mt-4'} flex flex-wrap gap-2 text-[11px] text-zinc-500`}>
      {result.sources
        .filter(s => s.status !== 'skipped') // an opt-in source that was not requested
        .map(s => (
          <span key={s.id} className="rounded-md border border-zinc-800 px-2 py-0.5" title={s.hosts?.join(', ')}>
            {SOURCE_LABELS[s.id] ?? s.id} · {chipState(s)}
            {s.sends_address ? ' · address sent' : s.id === 'bigquery_profile' ? ' · your GCP project' : ''}
          </span>
        ))}
    </div>
  );

  if (compact) {
    const compactGroups = GROUPS.filter(g => g.id === 'critical' || g.id === 'warning');
    return (
      <div className="space-y-2">
        <p role="status" className="text-xs text-zinc-200">
          <span className="mr-1.5 inline-flex items-center gap-1 rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] font-medium text-zinc-300 align-middle">
            <ChainIcon chain={chain} size={12} />
            <span>{chainLabel}</span>
          </span>
          {result.summary.text}
        </p>
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
                      <span className="inline-flex items-center gap-1.5 flex-wrap text-zinc-200">
                        <span className="text-zinc-500">{g.label} ·</span>
                        {c.token && <TokenIcon symbol={c.token} size={13} />}
                        <span>{c.title}</span>
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
    <Panel
      title={
        <span className="inline-flex items-center gap-2">
          <ChainIcon chain={chain} size={16} />
          <span>{`Result · ${chainLabel}`}</span>
        </span>
      }
      note={`checked ${ago(result.queried_at)}`}
    >
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
                  {clues.some(c => c.source === 'bigquery_profile') && (
                    <p>BigQuery profile clues come from Google&apos;s public dataset (full history); see the BigQuery profile card below.</p>
                  )}
                </div>
              )}
              <ul className="mt-2 space-y-2">
                {clues.map((c, i) => (
                  <li key={`${c.source}-${c.flag}-${i}`} className="rounded-lg border border-zinc-800 p-3 text-xs">
                    <div className="flex items-start justify-between gap-3">
                      <span className="inline-flex items-center gap-1.5 flex-wrap text-zinc-200">
                        <span className="text-zinc-500">{g.label} ·</span>
                        {c.token && <TokenIcon symbol={c.token} size={13} />}
                        <span>{c.title}</span>
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

      {result.bq_profile && <BQProfileCard p={result.bq_profile} onInspect={onInspect} />}

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
        {RISK_CHAINS.find(c => c.id === chain)?.family === 'evm' && (
          <p className="mt-3 text-[11px] text-zinc-500">
            Check your own wallet's token approvals →{' '}
            <a href={REVOKE_CASH_URL} target="_blank" rel="noopener noreferrer" className="text-zinc-300 hover:text-white">
              Revoke.cash
            </a>
          </p>
        )}
      </div>
    </Panel>
  );
}

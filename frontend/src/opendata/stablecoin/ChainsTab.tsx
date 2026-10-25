import { useState } from 'react';
import { usePolling } from '../common/usePolling';
import { fmtPct } from '../common/format';
import { fetchStableChains, fetchStableOnchain } from './api';
import { datasetHref } from './deeplink';
import type { StableAsset } from './deeplink';
import { fmtDateTime, fmtMoney, fmtNative, fmtShortAddr } from './format';
import SignalChip from './SignalChip';
import StableBigQueryLocked from './StableBigQueryLocked';
import { cx } from './styles';
import type { StableChains, StableMatrixRow, StableMetaBigQuery, StableOnchainResponse } from './types';
import { ColdState, CsvButton, ErrorNote, ExtLink, Panel } from './ui';
import type { StableBigQueryControl } from './useStableBigQuery';

// Chains (stablecoin.md §7.2, tab 5): where each asset lives, whether it is
// native or bridged, transfer costs where BigLens prices the chain, freeze
// mechanisms and explorer links, with a "Verify a payment" hand-off to
// Payment Check. Top holders and concentration are v1.1.

// Rows shown before "Show all": registry deployments (native and bridged)
// always, then the largest other chains up to this many rows.
const FOLDED_ROWS = 15;

function costText(c: StableChains['assets'][number]['rows'][number]['transfer_cost']): string {
  if (!c) return '—';
  if (c.usd === null) return c.status === 'not_cached' ? 'not measured yet' : c.status;
  return `${fmtMoney(c.usd, 'USD', 4)}${c.status === 'stale' ? ' · stale' : ''}`;
}

function OnchainActivitySection({
  asset,
  onchain,
  loading,
  error,
}: {
  asset: StableAsset;
  onchain: StableOnchainResponse | null;
  loading: boolean;
  error: string | null;
}) {
  if (error && !onchain) {
    return <ErrorNote message={error} />;
  }
  if (!onchain && loading) {
    return <ColdState>Loading BigQuery on-chain activity from SQLite…</ColdState>;
  }
  if (!onchain || (!onchain.data_available && onchain.daily.length === 0)) {
    return <ColdState>No BigQuery on-chain activity rows synced in SQLite yet.</ColdState>;
  }
  const rows = [...onchain.daily].reverse();
  return (
    <div className="space-y-6">
      {error && <ErrorNote message={error} />}
      <Panel
        title="BigQuery on-chain activity (daily)"
        help="Daily ERC-20 / TRC-20 transfer rollups from BigQuery (Ethereum USDT/USDC/EURC and TRON USDT), excluding zero-address mint/burn legs from transfer count and volume. Velocity = daily volume ÷ chain circulating supply."
        right={
          <CsvButton
            filename={`stablecoin-onchain-daily-${asset}.csv`}
            label="BigQuery daily on-chain activity"
            headers={[
              'day',
              'asset',
              'chain',
              'transfers',
              'zero_value',
              'volume_native',
              'senders',
              'receivers',
              'ge_1m_count',
              'ge_1m_volume_native',
              'velocity',
              'minted_native',
              'burned_native',
            ]}
            rows={() =>
              rows.map(d => [
                d.day,
                d.asset,
                d.chain,
                d.transfers,
                d.zero_value,
                d.volume_native,
                d.senders,
                d.receivers,
                d.ge_1m_count,
                d.ge_1m_volume_native,
                d.velocity ?? null,
                d.minted_native,
                d.burned_native,
              ])
            }
          />
        }
      >
        <div className="max-h-[360px] overflow-auto">
          <table className={cx.table}>
            <thead>
              <tr>
                {['Day', 'Asset', 'Chain', 'Transfers', 'Volume', 'Senders / Receivers', 'Transfers ≥ 1M', 'Velocity', 'Minted / Burned'].map(h => (
                  <th key={h} scope="col" className={cx.th}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map(d => {
                const sym = d.asset.toUpperCase();
                return (
                  <tr key={`${d.day}-${d.asset}-${d.chain}`} className={cx.tr}>
                    <td className={`${cx.td} font-mono`}>{d.day}</td>
                    <td className={cx.td}>{sym}</td>
                    <td className={cx.td}>{d.chain.toUpperCase()}</td>
                    <td className={`${cx.td} font-mono`}>{d.transfers.toLocaleString('en-US')}</td>
                    <td className={`${cx.td} font-mono`}>{fmtNative(d.volume_native, sym)}</td>
                    <td className={`${cx.td} font-mono`}>
                      {d.senders.toLocaleString('en-US')} / {d.receivers.toLocaleString('en-US')}
                    </td>
                    <td className={`${cx.td} font-mono`}>
                      {d.ge_1m_count.toLocaleString('en-US')} ({fmtNative(d.ge_1m_volume_native, sym)})
                    </td>
                    <td className={`${cx.td} font-mono`}>{d.velocity != null ? `${d.velocity.toFixed(3)}×` : '—'}</td>
                    <td className={`${cx.td} font-mono`}>
                      {fmtNative(d.minted_native, sym)} / {fmtNative(d.burned_native, sym)}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        <p className={`mt-2 text-[11px] ${cx.muted}`}>{onchain.coverage_note}</p>
      </Panel>

      <Panel
        title="Large transfers"
        help="Top 20 non-mint/burn transfers per token-chain-day from BigQuery (kept 365 days in SQLite)."
        right={
          <CsvButton
            filename={`stablecoin-large-transfers-${asset}.csv`}
            label="BigQuery large transfers"
            headers={['day', 'ts', 'asset', 'chain', 'amount_native', 'from', 'to', 'tx_hash', 'explorer_url']}
            rows={() =>
              onchain.large_transfers.map(t => [
                t.day,
                t.ts,
                t.asset,
                t.chain,
                t.amount_native,
                t.from,
                t.to,
                t.tx_hash,
                t.explorer_url ?? '',
              ])
            }
          />
        }
      >
        {onchain.large_transfers.length === 0 ? (
          <ColdState>No large transfers recorded in the selected window.</ColdState>
        ) : (
          <div className="max-h-[320px] overflow-auto">
            <table className={cx.table}>
              <thead>
                <tr>
                  {['Time', 'Asset', 'Chain', 'Amount', 'From → To', 'Tx'].map(h => (
                    <th key={h} scope="col" className={cx.th}>
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {onchain.large_transfers.map(t => (
                  <tr key={`${t.chain}-${t.tx_hash}-${t.log_index}`} className={cx.tr}>
                    <td className={`${cx.td} font-mono`}>{fmtDateTime(t.ts || t.day)}</td>
                    <td className={cx.td}>{t.asset.toUpperCase()}</td>
                    <td className={cx.td}>{t.chain.toUpperCase()}</td>
                    <td className={`${cx.td} font-mono`}>{fmtNative(t.amount_native, t.asset.toUpperCase())}</td>
                    <td className={`${cx.td} ${cx.mono}`} title={`${t.from} → ${t.to}`}>
                      {fmtShortAddr(t.from)} → {fmtShortAddr(t.to)}
                    </td>
                    <td className={cx.td}>
                      <ExtLink href={t.explorer_url}>
                        <span className={cx.mono}>{fmtShortAddr(t.tx_hash)}</span>
                      </ExtLink>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
    </div>
  );
}

export default function ChainsTab({
  asset,
  active,
  refreshTick,
  matrix,
  bqControl,
  bigquery,
}: {
  asset: StableAsset;
  active: boolean;
  refreshTick: number;
  matrix: StableMatrixRow[];
  bqControl?: StableBigQueryControl;
  bigquery?: StableMetaBigQuery | null;
}) {
  const matrixHasSupply = matrix.some(r => r.signals.some(s => s.id === 'S12' && Boolean(s.as_of)));
  const poll = usePolling(
    () => fetchStableChains(asset),
    60_000,
    `stable-chains-${asset}-${refreshTick}-${matrixHasSupply ? 'warm' : 'cold'}`,
    active,
  );
  const showBQ = Boolean(bqControl?.show);
  const onchainPoll = usePolling(
    () => fetchStableOnchain(asset, 90),
    60_000,
    `stable-onchain-${asset}-${refreshTick}-${showBQ ? 'on' : 'off'}`,
    active && showBQ,
  );
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const data = poll.data;
  if (!data) {
    return poll.error ? <ErrorNote message={poll.error} /> : <ColdState>Loading chains…</ColdState>;
  }
  return (
    <div className="space-y-6">
      {poll.error && <ErrorNote message={poll.error} />}
      {data.warming_up && <ColdState>Waiting for the first supply sample: issuer amounts per chain appear after it.</ColdState>}
      {data.assets.map(a => {
        const s10 = matrix.find(r => r.asset === a.asset)?.signals.find(s => s.id === 'S10');
        const registry = a.rows.filter(r => r.tier === 'native' || r.tier === 'bridged');
        const others = a.rows.filter(r => r.tier !== 'native' && r.tier !== 'bridged');
        const open = expanded[a.asset] ?? false;
        const shownOthers = open ? others : others.slice(0, Math.max(0, FOLDED_ROWS - registry.length));
        const shown = [...registry, ...shownOthers];
        return (
          <Panel
            key={a.asset}
            title={`${a.symbol} by chain`}
            help="Representation: native (issued by the issuer on that chain) or bridged (a claim on a bridge contract, e.g. USDC.e). Transfer cost: BigLens' gas-live estimate for a token transfer where it prices the chain (stale after 1 h, kept 24 h). Freeze mechanism: the check Address Risk and Payment Check run. S10 watches bridged representations holding ≥ 10% of supply."
            right={
              <>
                {s10 && <SignalChip state={s10.state} prefix="S10" title={s10.summary || s10.rule} />}
                <CsvButton
                  filename={`stablecoin-chain-table-${a.asset}.csv`}
                  label={`${a.symbol} chain table`}
                  headers={['chain', 'tier', 'token_label', 'contract', 'issuer_circulating_native', 'issuer_share_pct', 'aggregator_circulating_native', 'transfer_cost_usd', 'transfer_cost_status', 'freeze_kind', 'freeze_function', 'explorer_url']}
                  rows={() =>
                    a.rows.map(r => [
                      r.chain_label,
                      r.tier,
                      r.token_label ?? '',
                      r.contract ?? '',
                      r.issuer_circulating_native,
                      r.issuer_share_pct,
                      r.aggregator_circulating_native,
                      r.transfer_cost?.usd ?? null,
                      r.transfer_cost?.status ?? '',
                      r.freeze_mechanism?.kind ?? '',
                      r.freeze_mechanism?.function ?? '',
                      r.explorer_url ?? '',
                    ])
                  }
                />
              </>
            }
          >
            {a.rows.length === 0 ? (
              <ColdState>No chains yet.</ColdState>
            ) : (
              <div className="overflow-x-auto">
                <table className={cx.table}>
                  <thead>
                    <tr>
                      {['Chain', 'Representation', 'Issuer circulating', 'Share', 'Transfer cost', 'Freeze mechanism', 'Contract'].map(h => (
                        <th key={h} scope="col" className={cx.th}>
                          {h}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {shown.map(r => (
                      <tr key={`${r.chain}-${r.contract ?? r.tier}`} className={cx.tr}>
                        <td className={cx.td}>
                          <span className={`font-medium ${cx.strong}`}>{r.chain_label}</span>
                          {r.verified && <span className={`ml-1 text-[10px] ${cx.muted}`}>· read on-chain</span>}
                          {r.verify_payment && (
                            <a href={datasetHref('crypto', r.verify_payment.hash)} className={`ml-2 text-[11px] ${cx.link} ${cx.focus}`}>
                              Verify a payment
                            </a>
                          )}
                        </td>
                        <td className={cx.td}>
                          {r.tier}
                          {r.token_label ? ` · ${r.token_label}` : ''}
                        </td>
                        <td className={`${cx.td} font-mono`}>{fmtNative(r.issuer_circulating_native ?? r.aggregator_circulating_native, a.symbol)}</td>
                        <td className={`${cx.td} font-mono`}>{fmtPct(r.issuer_share_pct, 2)}</td>
                        <td
                          className={`${cx.td} font-mono`}
                          title={r.transfer_cost ? `${r.transfer_cost.label} · ${r.transfer_cost.source}${r.transfer_cost.as_of ? ` · read ${fmtDateTime(r.transfer_cost.as_of)}` : ''}` : undefined}
                        >
                          {costText(r.transfer_cost)}
                        </td>
                        <td className={cx.td}>
                          {r.freeze_mechanism ? (
                            <span title={r.freeze_mechanism.controls_monitored ? 'Controls sampler reads this deployment' : undefined}>
                              {r.freeze_mechanism.kind.replace(/_/g, ' ')} <span className={`${cx.mono} ${cx.muted}`}>{r.freeze_mechanism.function}</span>
                            </span>
                          ) : (
                            '—'
                          )}
                        </td>
                        <td className={cx.td}>
                          {r.contract ? (
                            <ExtLink href={r.explorer_url}>
                              <span className={cx.mono}>{fmtShortAddr(r.contract)}</span>
                            </ExtLink>
                          ) : (
                            '—'
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            {others.length > shownOthers.length || open ? (
              <button type="button" onClick={() => setExpanded(e => ({ ...e, [a.asset]: !open }))} className={`${cx.button} ${cx.focus} mt-2`}>
                {open ? 'Show fewer chains' : `Show all ${a.rows.length} chains (${others.length - shownOthers.length} more, mostly DefiLlama-only)`}
              </button>
            ) : null}
            <p className={`mt-2 text-[11px] ${cx.faint}`}>
              Issuer amounts as of {fmtDateTime(a.issuer_as_of)} · total {fmtNative(a.issuer_circulating_native, a.symbol)}
            </p>
          </Panel>
        );
      })}
      {showBQ ? (
        <OnchainActivitySection
          asset={asset}
          onchain={onchainPoll.data}
          loading={onchainPoll.loading}
          error={onchainPoll.error}
        />
      ) : (
        <StableBigQueryLocked
          title="BigQuery On-Chain Activity (ETH & TRON)"
          description="Daily transfers, transfer volume, unique senders/receivers, transfers ≥ 1M, velocity, and top 20 large transfers per day from Ethereum (USDT, USDC, EURC) and TRON (USDT). Off by default; reads pre-synced SQLite tables only."
          bigquery={bigquery}
          onEnableDisplay={bqControl?.available ? bqControl.toggle : undefined}
        />
      )}
      <p className={`text-[11px] ${cx.muted}`}>
        {data.cost_note} Bridged tokens are claims on bridge contracts, not the issuer.
      </p>
    </div>
  );
}

import { usePolling } from '../common/usePolling';
import { fetchStableControls } from './api';
import ControlTable from './ControlTable';
import { datasetHref } from './deeplink';
import type { StableAsset } from './deeplink';
import { fmtDateTime, fmtNative, fmtShortAddr } from './format';
import SignalChip from './SignalChip';
import { cx } from './styles';
import type { StableControls, StableMatrixRow, StableScamChainCounters } from './types';
import { ColdState, CsvButton, ErrorNote, ExtLink, Panel, Stat } from './ui';

// Controls & Freezes (stablecoin.md §7.2, tab 6): contract control state per
// deployment (RPC, confirmed), the change log, freeze activity from the
// Address Risk sync, Tether's quarantined amounts, and Scam Radar's
// counterfeit and poisoning counters.

function ScamCounters({ label, c }: { label: string; c: StableScamChainCounters | null }) {
  if (!c) return <Stat label={label} value="—" sub="not available" />;
  if (c.status !== 'ok') return <Stat label={label} value="—" sub={`Scam Radar: ${c.status.replace('_', ' ')}`} />;
  const contracts = Object.entries(c.counterfeit_contracts ?? {});
  return (
    <div className={cx.inset}>
      <div className={`text-[11px] ${cx.muted}`}>{label}</div>
      <div className={`mt-1 grid grid-cols-2 gap-x-4 gap-y-0.5 text-xs ${cx.text}`}>
        <span>Poisoning hits</span>
        <span className="text-right font-mono">{c.poison_hits === null ? '—' : c.poison_hits.toLocaleString('en-US')}</span>
        <span>Poisoning victims</span>
        <span className="text-right font-mono">{c.poison_victims === null ? '—' : c.poison_victims.toLocaleString('en-US')}</span>
        {contracts.map(([sym, n]) => (
          <span key={sym} className="col-span-2 flex justify-between">
            <span>Counterfeit {sym} contracts</span>
            <span className="font-mono">{n}</span>
          </span>
        ))}
        {c.counterfeit_transfers != null && (
          <>
            <span>Counterfeit transfers</span>
            <span className="text-right font-mono">{c.counterfeit_transfers.toLocaleString('en-US')}</span>
          </>
        )}
      </div>
      <div className={`mt-1 text-[10px] ${cx.faint}`}>
        {c.from} → {c.to} · {c.days_with_data} days with data{c.partial ? ' (history starts inside the window)' : ''}
      </div>
    </div>
  );
}

function Freezes({ data }: { data: StableControls }) {
  return (
    <Panel
      title="Freeze activity"
      help="Freezes, unfreezes and destroyed funds from the Address Risk blacklist sync (Ethereum logs and TRON), over 30 and 90 days. Freezing is a designed feature: activity is context, not failure. Coverage notes say where a token's history starts."
      right={
        <CsvButton
          filename="stablecoin-freeze-events.csv"
          label="recent freeze events"
          headers={['asset', 'chain', 'action', 'address', 'amount', 'block_time', 'tx_hash', 'tx_url']}
          rows={() => data.freezes.flatMap(f => f.recent.map(e => [f.asset, f.chain, e.action, e.address, e.amount ?? '', e.block_time, e.tx_hash, e.tx_url]))}
        />
      }
    >
      {data.freezes.length === 0 ? (
        <ColdState>No freeze sources for this selection.</ColdState>
      ) : (
        <div className="space-y-4">
          {data.freezes.map(f => (
            <div key={`${f.asset}-${f.chain}`} className={cx.inset}>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className={`text-xs font-semibold ${cx.strong}`}>
                  {f.token} on {f.chain.toUpperCase()}
                </span>
                <span className={`text-[11px] ${cx.muted}`}>
                  {f.status === 'ok' ? `frozen now: ${f.frozen_now ?? '—'}` : 'not synced yet'}
                  {f.coverage_from && ` · coverage from ${f.coverage_from}`}
                </span>
              </div>
              {f.coverage_note && <p className={`mt-1 text-[11px] ${cx.muted}`}>{f.coverage_note}</p>}
              <div className="mt-2 grid grid-cols-1 gap-2 md:grid-cols-2">
                {f.windows.map(w => (
                  <div key={w.days} className={`text-xs ${cx.text}`}>
                    <span className={`font-medium ${cx.strong}`}>{w.days} days</span>
                    {w.partial && <span className={cx.muted}> (partial)</span>}: {w.freezes} freezes · {w.unfreezes} unfreezes · {w.destroys} destroyed
                    {w.destroyed_native ? ` (${w.destroyed_native} ${f.token})` : ''}
                  </div>
                ))}
              </div>
              {f.recent.length > 0 && (
                <div className="mt-2 max-h-[200px] overflow-auto">
                  <table className={cx.table}>
                    <thead>
                      <tr>
                        {['Time', 'Action', 'Address', 'Amount', 'Tx'].map(h => (
                          <th key={h} scope="col" className={cx.th}>
                            {h}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {f.recent.map(e => (
                        <tr key={`${e.tx_hash}-${e.address}`} className={cx.tr}>
                          <td className={`${cx.td} font-mono`}>{fmtDateTime(e.block_time)}</td>
                          <td className={cx.td}>{e.action}</td>
                          <td className={`${cx.td} ${cx.mono}`} title={e.address}>
                            {fmtShortAddr(e.address)}
                          </td>
                          <td className={`${cx.td} font-mono`}>{e.amount || '—'}</td>
                          <td className={cx.td}>
                            <ExtLink href={e.tx_url}>
                              <span className={cx.mono}>{fmtShortAddr(e.tx_hash)}</span>
                            </ExtLink>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          ))}
        </div>
      )}
      <p className={`mt-3 text-[11px] ${cx.muted}`}>
        Check an address against these blacklists in <a href={datasetHref('crypto')} className={`${cx.link} ${cx.focus}`}>Crypto Pulse → Address Risk</a>.
      </p>
    </Panel>
  );
}

export default function ControlsTab({
  asset,
  active,
  refreshTick,
  matrix,
}: {
  asset: StableAsset;
  active: boolean;
  refreshTick: number;
  matrix: StableMatrixRow[];
}) {
  const poll = usePolling(() => fetchStableControls(asset), 60_000, `stable-controls-${asset}-${refreshTick}`, active);
  const data = poll.data;
  if (!data) {
    return poll.error ? <ErrorNote message={poll.error} /> : <ColdState>Loading controls…</ColdState>;
  }
  return (
    <div className="space-y-6">
      {poll.error && <ErrorNote message={poll.error} />}
      <p className={`text-[11px] ${cx.muted}`}>
        Controls provider: {data.provider_status} · newest read {fmtDateTime(data.as_of)}. Every monitored deployment can freeze addresses; USDC and EURC are upgradeable
        proxies; USDT on Ethereum can be deprecated in favor of an upgraded address; all three are pausable.
      </p>
      {data.assets.map(a => {
        const s8 = matrix.find(r => r.asset === a.asset)?.signals.find(s => s.id === 'S8');
        return (
          <Panel
            key={a.asset}
            title={`${a.symbol} contract controls`}
            help="Owner, proxy admin, implementation, masterMinter, pauser, blacklister, rescuer, paused, deprecated (USDT) and SPL authorities, read over the configured RPCs every 15 minutes. A change counts only when two consecutive samples or two RPC endpoints agree; a zero address where one is not allowed is a read error. S8: warning for 7 days after a change; critical when paused or deprecated."
            right={s8 && <SignalChip state={s8.state} prefix="S8" title={s8.summary || s8.rule} />}
          >
            {a.deployments.length === 0 ? (
              <ColdState>No monitored deployments.</ColdState>
            ) : (
              <div className="space-y-3">
                {a.deployments.map(d => (
                  <ControlTable key={`${d.chain}-${d.contract}`} d={d} />
                ))}
              </div>
            )}
          </Panel>
        );
      })}
      <Panel
        title="Change log"
        help="Confirmed control changes detected over RPC, newest first (at most 100). BigQuery tx-linked admin events arrive with the v1.1 jobs."
        right={
          <CsvButton
            filename={`stablecoin-control-changes-${asset}.csv`}
            label="control changes"
            headers={['detected_at', 'asset', 'chain', 'field', 'old', 'new', 'first_seen', 'confirmed_by', 'critical', 'tx_hash']}
            rows={() => data.changes.map(c => [c.detected_at, c.asset, c.chain, c.field, c.old, c.new, c.first_seen, c.confirmed_by, c.critical ? 'yes' : 'no', c.tx_hash ?? ''])}
          />
        }
      >
        {data.changes.length === 0 ? (
          <ColdState>No confirmed changes since monitoring began. No change can be reported before the first confirmed baseline.</ColdState>
        ) : (
          <div className="max-h-[320px] overflow-auto">
            <table className={cx.table}>
              <thead>
                <tr>
                  {['Detected', 'Asset', 'Chain', 'Field', 'Old → new', 'Confirmed by', ''].map(h => (
                    <th key={h || 'crit'} scope="col" className={cx.th}>
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {data.changes.map((c, i) => (
                  <tr key={`${c.detected_at}-${c.field}-${i}`} className={cx.tr}>
                    <td className={`${cx.td} font-mono`}>{fmtDateTime(c.detected_at)}</td>
                    <td className={cx.td}>{c.asset.toUpperCase()}</td>
                    <td className={cx.td}>{c.chain}</td>
                    <td className={cx.td}>{c.field}</td>
                    <td className={`${cx.td} ${cx.mono}`} title={`${c.old} → ${c.new}`}>
                      {fmtShortAddr(c.old)} → {fmtShortAddr(c.new)}
                    </td>
                    <td className={cx.td}>{c.confirmed_by.replace('_', ' ')}</td>
                    <td className={cx.td}>{c.critical && <SignalChip state="critical" />}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
      <Freezes data={data} />
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Panel title="Tether quarantined" help="Tether's `quarantined` amounts per chain from its transparency JSON (latest update).">
          {!data.quarantined ? (
            <ColdState>No Tether update yet (or USDT not selected).</ColdState>
          ) : (
            <>
              <Stat label="Quarantined (all chains)" value={fmtNative(data.quarantined.total_native, 'USDT')} sub={`update ${fmtDateTime(data.quarantined.as_of)}`} />
              <ul className={`mt-2 space-y-0.5 text-xs ${cx.text}`}>
                {data.quarantined.chains
                  .filter(q => q.quarantined_native > 0)
                  .map(q => (
                    <li key={q.chain} className="flex justify-between gap-3">
                      <span>{q.chain_label}</span>
                      <span className="font-mono">{fmtNative(q.quarantined_native, 'USDT')}</span>
                    </li>
                  ))}
              </ul>
              {data.quarantined.chains.some(q => q.quarantined_native === 0) && (
                <p className={`mt-1 text-[11px] ${cx.muted}`}>
                  {data.quarantined.chains.filter(q => q.quarantined_native === 0).length} other chains report 0 quarantined.
                </p>
              )}
            </>
          )}
        </Panel>
        <Panel title="Counterfeits and poisoning" help={data.scam.note || 'Scam Radar counters over the window: address-poisoning hits and victims, counterfeit token contracts and transfers.'}>
          <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
            <ScamCounters label={`Ethereum (USDT, USDC) · ${data.scam.window_days} days`} c={data.scam.eth} />
            <ScamCounters label={`TRON (USDT) · ${data.scam.window_days} days`} c={data.scam.tron} />
          </div>
          <p className={`mt-2 text-[11px] ${cx.muted}`}>
            Always verify the contract address and the recipient: <a href={datasetHref('crypto', '#pay')} className={`${cx.link} ${cx.focus}`}>Payment Check</a>.
          </p>
        </Panel>
      </div>
    </div>
  );
}

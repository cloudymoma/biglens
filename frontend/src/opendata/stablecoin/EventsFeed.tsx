import { useState } from 'react';
import type { StableAsset } from './deeplink';
import { fmtDateTime } from './format';
import SignalChip from './SignalChip';
import { cx } from './styles';
import type { StableEvent } from './types';
import { ColdState, CsvButton, ExtLink, Panel } from './ui';

const KIND_LABEL: Record<string, string> = {
  control_change: 'control change',
  control: 'control',
  freeze: 'freeze',
  peg_state: 'peg state',
  supply_move: 'supply move',
  attestation: 'attestation',
  regulatory: 'regulatory',
  incident: 'incident',
  depeg: 'depeg',
  key_date: 'key date',
};

const SHOW = 40;

// Events feed (stablecoin.md §7.2): control changes, freezes, depeg state
// changes, large supply moves and curated events, newest first.
export default function EventsFeed({ events, asset }: { events: StableEvent[]; asset: StableAsset }) {
  const [all, setAll] = useState(false);
  const rows = asset === 'all' ? events : events.filter(e => e.asset === asset || e.asset === 'all');
  const shown = all ? rows : rows.slice(0, SHOW);
  return (
    <Panel
      title="Events"
      help="Control changes (RPC, confirmed), freeze events (Address Risk), depeg state changes (signals), issuer snapshot deltas of at least $250M or €25M within an hour, and curated attestation, regulatory and incident events. Newest first."
      right={
        <CsvButton
          filename={`stablecoin-events-${asset}.csv`}
          label="events"
          headers={['ts', 'asset', 'severity', 'kind', 'title', 'source', 'url']}
          rows={() => rows.map(e => [e.ts, e.asset, e.severity, e.kind, e.title, e.source, e.url ?? ''])}
        />
      }
    >
      {rows.length === 0 ? (
        <ColdState>No events recorded yet. Control changes, depeg state changes and large supply moves appear here as they happen.</ColdState>
      ) : (
        <>
          <div className="max-h-[420px] overflow-auto">
            <table className={cx.table}>
              <thead>
                <tr>
                  <th scope="col" className={cx.th}>Time</th>
                  <th scope="col" className={cx.th}>Asset</th>
                  <th scope="col" className={cx.th}>Severity</th>
                  <th scope="col" className={cx.th}>Kind</th>
                  <th scope="col" className={cx.th}>Event</th>
                  <th scope="col" className={cx.th}>Source</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((e, i) => (
                  <tr key={`${e.ts}-${e.kind}-${i}`} className={cx.tr}>
                    <td className={`${cx.td} font-mono`}>{e.source === 'facts' ? e.ts.slice(0, 10) : fmtDateTime(e.ts)}</td>
                    <td className={cx.td}>{e.asset === 'all' ? 'all' : e.asset.toUpperCase()}</td>
                    <td className={cx.td}>
                      <SignalChip state={e.severity} />
                    </td>
                    <td className={cx.td}>{KIND_LABEL[e.kind] ?? e.kind}</td>
                    <td className={`${cx.tdWrap}`}>
                      {e.url ? <ExtLink href={e.url}>{e.title}</ExtLink> : e.title}
                    </td>
                    <td className={cx.td}>{e.source}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {rows.length > SHOW && (
            <button type="button" onClick={() => setAll(v => !v)} className={`${cx.button} ${cx.focus} mt-2`}>
              {all ? `Show the newest ${SHOW}` : `Show all ${rows.length} events`}
            </button>
          )}
        </>
      )}
    </Panel>
  );
}

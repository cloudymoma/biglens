import { fmtDateTime, fmtShortAddr } from './format';
import { cx } from './styles';
import type { StableControlDeployment, StableControlField } from './types';
import { CsvButton, ExtLink } from './ui';

// ControlTable (stablecoin.md §7.2, tab 6): one deployment's control state.
// Values are the confirmed ones (a change needs 2 consecutive samples or 2
// RPC endpoints agreeing); nothing is reported before the first confirmed
// baseline ("Monitoring since …").

const FIELD_LABEL: Record<string, string> = {
  owner: 'owner',
  admin: 'proxy admin',
  implementation: 'implementation',
  master_minter: 'masterMinter',
  pauser: 'pauser',
  blacklister: 'blacklister',
  rescuer: 'rescuer',
  paused: 'paused',
  deprecated: 'deprecated',
  upgraded_address: 'upgradedAddress',
  mint_authority: 'SPL mint authority',
  freeze_authority: 'SPL freeze authority',
};

function FieldValue({ f }: { f: StableControlField }) {
  if (f.value === null) return <span className={cx.muted}>{f.read_error ? 'read error' : 'awaiting baseline'}</span>;
  if (f.unset) return <span className={cx.muted}>not set{f.value ? ` (${fmtShortAddr(f.value)})` : ''}</span>;
  if (f.value === 'true' || f.value === 'false') return <span className="font-mono">{f.value}</span>;
  return f.explorer_url ? (
    <ExtLink href={f.explorer_url}>
      <span className={cx.mono} title={f.value}>
        {fmtShortAddr(f.value)}
      </span>
    </ExtLink>
  ) : (
    <span className={cx.mono} title={f.value}>
      {fmtShortAddr(f.value)}
    </span>
  );
}

function fieldStatus(f: StableControlField): string {
  if (f.pending) return `pending confirmation: ${fmtShortAddr(f.pending.value)} since ${fmtDateTime(f.pending.first_seen)}${f.pending.disputed ? ' (RPCs disagree)' : ''}`;
  if (f.read_error) return `read error (${f.read_error})${f.value !== null ? ', showing the last confirmed value' : ''}`;
  if (f.value === null) return 'no confirmed reading yet';
  if (f.baseline) return 'unchanged since monitoring began';
  return `changed: confirmed ${fmtDateTime(f.confirmed_at)} by ${f.confirmed_by === 'two_rpcs' ? 'two RPCs' : 'two samples'}`;
}

export default function ControlTable({ d }: { d: StableControlDeployment }) {
  return (
    <div className={cx.inset}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className={`text-xs font-semibold ${cx.strong}`}>
          {d.asset.toUpperCase()} on {d.chain_label}{' '}
          <span className={`font-normal ${cx.muted}`}>
            ({d.kind}) ·{' '}
            <ExtLink href={d.explorer_url}>
              <span className={cx.mono}>{fmtShortAddr(d.contract)}</span>
            </ExtLink>
          </span>
        </div>
        <div className={`flex flex-wrap items-center gap-2 text-[11px] ${cx.muted}`}>
          <span>
            {d.monitoring_since ? `Monitoring since ${fmtDateTime(d.monitoring_since)}` : 'Waiting for the first confirmed snapshot'}
            {d.last_change && ` · last change ${fmtDateTime(d.last_change)}`}
          </span>
          <CsvButton
            filename={`stablecoin-controls-${d.asset}-${d.chain}.csv`}
            label={`${d.asset.toUpperCase()} ${d.chain_label} control state`}
            headers={['asset', 'chain', 'contract', 'field', 'value', 'unset', 'baseline', 'first_seen', 'confirmed_at', 'confirmed_by', 'last_read_at', 'read_error', 'pending_value', 'endpoint']}
            rows={() =>
              d.fields.map(f => [
                d.asset,
                d.chain,
                d.contract,
                f.field,
                f.value,
                f.unset ? 'yes' : 'no',
                f.baseline ? 'yes' : 'no',
                f.first_seen ?? '',
                f.confirmed_at ?? '',
                f.confirmed_by ?? '',
                f.last_read_at ?? '',
                f.read_error ?? '',
                f.pending?.value ?? '',
                f.endpoint ?? '',
              ])
            }
          />
        </div>
      </div>
      <div className={`mt-1 text-[11px] ${cx.text}`}>
        Paused: <span className="font-mono">{d.paused === null ? '—' : String(d.paused)}</span>
        {d.deprecated !== undefined && (
          <>
            {' '}
            · Deprecated: <span className="font-mono">{d.deprecated === null ? '—' : String(d.deprecated)}</span>
          </>
        )}
        {d.changed_last_7d.length > 0 && (
          <span className="ml-2 text-amber-800 dark:text-amber-300">Changed in the last 7 days: {d.changed_last_7d.join(', ')}</span>
        )}
      </div>
      <div className="mt-2 overflow-x-auto">
        <table className={cx.table}>
          <thead>
            <tr>
              {['Field', 'Value', 'Status', 'First seen', 'Last read', 'Endpoint'].map(h => (
                <th key={h} scope="col" className={cx.th}>
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {d.fields.map(f => (
              <tr key={f.field} className={cx.tr}>
                <td className={cx.td}>{FIELD_LABEL[f.field] ?? f.field}</td>
                <td className={cx.td}>
                  <FieldValue f={f} />
                  {f.stale && <span className={`ml-1 text-[10px] ${cx.muted}`}>· stale</span>}
                </td>
                <td className={`${cx.tdWrap}`}>{fieldStatus(f)}</td>
                <td className={`${cx.td} font-mono`}>{fmtDateTime(f.first_seen)}</td>
                <td className={`${cx.td} font-mono`}>{fmtDateTime(f.last_read_at)}</td>
                <td className={`${cx.td} ${cx.mono}`}>{f.endpoint || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

import { useState } from 'react';
import { fmtDateTime } from './format';
import SignalChip from './SignalChip';
import { cx } from './styles';
import type { StableMatrixRow, StableSignal } from './types';

// Risk matrix (stablecoin.md §7.2): assets × signals, no composite score.
// Each cell shows the state word and icon; selecting a cell shows the rule,
// summary, inputs and as-of time below the table (keyboard and screen-reader
// friendly, unlike hover-only tooltips).

const sigNum = (id: string): number => Number(id.replace(/^S/, '')) || 0;

function fmtInput(v: unknown): string {
  if (v === null || v === undefined) return '—';
  if (typeof v === 'number') return Number.isInteger(v) ? String(v) : v.toFixed(4).replace(/0+$/, '').replace(/\.$/, '');
  if (typeof v === 'string') return v || '—';
  if (typeof v === 'boolean') return v ? 'yes' : 'no';
  return JSON.stringify(v);
}

function SignalDetail({ asset, signal }: { asset: string; signal: StableSignal }) {
  const inputs = Object.entries(signal.inputs ?? {});
  return (
    <div className={`${cx.inset} mt-3 space-y-2`} aria-live="polite">
      <div className="flex flex-wrap items-center gap-2">
        <span className={`text-xs font-semibold ${cx.strong}`}>
          {asset.toUpperCase()} · {signal.id} {signal.dimension}
        </span>
        <SignalChip state={signal.state} />
        <span className={`text-[11px] ${cx.muted}`}>policy: {signal.policy}</span>
      </div>
      {signal.summary && <p className={`text-xs ${cx.text}`}>{signal.summary}</p>}
      {signal.reason && <p className={`text-xs ${cx.muted}`}>Reason: {signal.reason}</p>}
      <p className={`text-xs ${cx.muted}`}>
        <span className="font-medium">Rule:</span> {signal.rule}
      </p>
      {inputs.length > 0 && (
        <dl className="grid grid-cols-1 gap-x-6 gap-y-0.5 sm:grid-cols-2">
          {inputs.map(([k, v]) => (
            <div key={k} className="flex justify-between gap-3 text-[11px]">
              <dt className={cx.muted}>{k}</dt>
              <dd className={`break-all text-right font-mono ${cx.text}`}>{fmtInput(v)}</dd>
            </div>
          ))}
        </dl>
      )}
      <p className={`text-[11px] ${cx.faint}`}>
        as of {fmtDateTime(signal.as_of)}
        {signal.since && ` · state since ${fmtDateTime(signal.since)}`}
      </p>
    </div>
  );
}

export default function SignalMatrix({ rows, only }: { rows: StableMatrixRow[]; only?: string[] }) {
  const [sel, setSel] = useState<{ asset: string; id: string } | null>(null);
  const ids: string[] = [];
  const dims: Record<string, string> = {};
  for (const r of rows) {
    for (const s of r.signals) {
      if (only && !only.includes(s.id)) continue;
      if (!dims[s.id]) {
        ids.push(s.id);
        dims[s.id] = s.dimension;
      }
    }
  }
  ids.sort((a, b) => sigNum(a) - sigNum(b));
  const selRow = sel ? rows.find(r => r.asset === sel.asset) : undefined;
  const selected = sel && selRow ? selRow.signals.find(s => s.id === sel.id) : undefined;

  // One asset: signals as rows, with their summaries (fits a narrow column).
  if (rows.length === 1) {
    const r = rows[0];
    return (
      <div>
        <table className={cx.table}>
          <caption className="sr-only">{r.asset.toUpperCase()} signal states. Select a state for its rule, inputs and as-of time.</caption>
          <thead>
            <tr>
              <th scope="col" className={cx.th}>
                Signal
              </th>
              <th scope="col" className={cx.th}>
                State
              </th>
              <th scope="col" className={cx.th}>
                Summary
              </th>
            </tr>
          </thead>
          <tbody>
            {!only && (
              <tr className={cx.tr}>
                <th scope="row" className={`${cx.td} font-semibold`}>
                  Worst
                </th>
                <td className={cx.td}>
                  <SignalChip state={r.worst} />
                </td>
                <td className={cx.tdWrap}>
                  <span className={cx.muted}>Worst state across the signals (info counts as normal).</span>
                </td>
              </tr>
            )}
            {ids.map(id => {
              const s = r.signals.find(x => x.id === id);
              if (!s) return null;
              const on = sel?.asset === r.asset && sel?.id === id;
              return (
                <tr key={id} className={cx.tr}>
                  <th scope="row" className={`${cx.td} font-medium`}>
                    {id} <span className={`font-normal ${cx.muted}`}>{s.dimension}</span>
                  </th>
                  <td className={cx.td}>
                    <button
                      type="button"
                      onClick={() => setSel(on ? null : { asset: r.asset, id })}
                      aria-pressed={on}
                      aria-label={`${id} ${s.dimension}: ${s.state}. ${on ? 'Hide' : 'Show'} details`}
                      className={`rounded-full ${cx.focus} ${on ? 'ring-2 ring-sky-500/60' : ''}`}
                    >
                      <SignalChip state={s.state} />
                    </button>
                  </td>
                  <td className={cx.tdWrap}>
                    <span className={cx.muted}>{s.summary || s.reason || '—'}</span>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
        {sel && selected && <SignalDetail asset={sel.asset} signal={selected} />}
      </div>
    );
  }

  return (
    <div>
      <div className="overflow-x-auto">
        <table className={cx.table}>
          <caption className="sr-only">Signal states per asset. Select a cell for its rule, inputs and as-of time.</caption>
          <thead>
            <tr>
              <th scope="col" className={cx.th}>
                Asset
              </th>
              {!only && (
                <th scope="col" className={cx.th}>
                  Worst
                </th>
              )}
              {ids.map(id => (
                <th key={id} scope="col" className={cx.th}>
                  {id}
                  <span className="block font-normal">{dims[id]}</span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map(r => (
              <tr key={r.asset} className={cx.tr}>
                <th scope="row" className={`${cx.td} font-semibold`}>
                  {r.asset.toUpperCase()}
                </th>
                {!only && (
                  <td className={cx.td}>
                    <SignalChip state={r.worst} />
                  </td>
                )}
                {ids.map(id => {
                  const s = r.signals.find(x => x.id === id);
                  const on = sel?.asset === r.asset && sel?.id === id;
                  return (
                    <td key={id} className={cx.td}>
                      {s ? (
                        <button
                          type="button"
                          onClick={() => setSel(on ? null : { asset: r.asset, id })}
                          aria-pressed={on}
                          aria-label={`${r.asset.toUpperCase()} ${id} ${s.dimension}: ${s.state}. ${on ? 'Hide' : 'Show'} details`}
                          title={s.summary || s.rule}
                          className={`rounded-full ${cx.focus} ${on ? 'ring-2 ring-sky-500/60' : ''}`}
                        >
                          <SignalChip state={s.state} />
                        </button>
                      ) : (
                        <span className={cx.faint}>—</span>
                      )}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {sel && selected && <SignalDetail asset={sel.asset} signal={selected} />}
    </div>
  );
}

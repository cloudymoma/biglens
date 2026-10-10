import { useEffect, useState } from 'react';
import type { BtcLive, GasCalibration, GasLiveData, L2Ladder, TronLive } from '../../types';
import { fetchGasLive } from '../../api';
import { ErrorBanner } from '../../dashboards/shared';
import { Panel } from './shared';
import { fmtGas } from './gasPulse';
import { ChainIcon, TokenIcon } from './CryptoIcons';

const LIVE_POLL_MS = 30_000;

// Fee bands from cheap to expensive.
const BAND_COLORS = ['#34d399', '#a3e635', '#facc15', '#fb923c', '#f87171', '#e11d48'];

// Keep sub-1 sat/vB precision: 0.2 must never render as 0.
const fmtRate = (v: number): string => v.toLocaleString('en', { maximumFractionDigits: 3 });

const fmtUSD = (v: number | null): string =>
  v == null ? '' : `$${v >= 0.01 ? v.toFixed(2) : v.toPrecision(2)}`;

const fmtAsOf = (iso: string): string => `${iso.slice(11, 19)} UTC`;

export default function GasLiveBar({ chain, highlight }: { chain: 'btc' | 'tron' | 'l2'; highlight?: string }) {
  const [data, setData] = useState<GasLiveData | null>(null);
  const [error, setError] = useState('');

  // Poll while mounted; the parent keys this component by chain, so switching
  // chains unmounts it and the cleanup stops the timer. Pause polling while
  // the browser tab is hidden and refresh when it becomes visible again.
  useEffect(() => {
    let alive = true;
    const load = () => {
      if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return;
      fetchGasLive(chain)
        .then(d => {
          if (!alive) return;
          setData(d);
          setError('');
        })
        .catch(e => {
          if (alive) setError(e.response?.data || e.message);
        });
    };
    const onVis = () => {
      if (typeof document !== 'undefined' && document.visibilityState === 'visible') load();
    };
    load();
    const id = setInterval(load, LIVE_POLL_MS);
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', onVis);
    }
    return () => {
      alive = false;
      clearInterval(id);
      if (typeof document !== 'undefined') {
        document.removeEventListener('visibilitychange', onVis);
      }
    };
  }, [chain]);

  if (error) return <ErrorBanner message={error} />;
  if (!data) return <div className="text-[11px] text-zinc-600">Loading live data…</div>;

  const note = `updated ${fmtAsOf(data.as_of)} · refreshes every 30s`;
  if (chain === 'btc') {
    return (
      <Panel
        title={
          <span className="inline-flex items-center gap-1.5">
            <ChainIcon chain="btc" size={16} />
            <span>Bitcoin live · mempool.space</span>
          </span>
        }
        note={note}
      >
        {data.btc ? <BtcLiveBody live={data.btc} asOf={data.as_of} /> : <ErrorBanner message={data.btc_error || 'unavailable'} />}
      </Panel>
    );
  }
  if (chain === 'l2') {
    return (
      <Panel title="L1 vs L2 · full transaction cost" note={note}>
        <L2LadderBody ladder={data.l2} highlight={highlight} />
      </Panel>
    );
  }
  return (
    <Panel
      title={
        <span className="inline-flex items-center gap-1.5">
          <ChainIcon chain="tron" size={16} />
          <TokenIcon symbol="USDT" size={16} />
          <span>TRON live · USDT transfer burn cost</span>
        </span>
      }
      note={note}
    >
      {data.tron ? <TronLiveBody live={data.tron} calibration={data.calibration} /> : <ErrorBanner message={data.tron_error || 'unavailable'} />}
    </Panel>
  );
}

function BtcLiveBody({ live, asOf }: { live: BtcLive; asOf: string }) {
  const total = live.bands.reduce((s, b) => s + b.vsize_mb, 0);
  return (
    <div className="space-y-4">
      <BtcConveyor live={live} asOf={asOf} />
      <div className="grid grid-cols-3 gap-3">
        {live.tiers.map(t => (
          <div key={t.label} className="rounded-xl border border-zinc-800/50 px-3 py-2" style={{ background: '#0c0c0f' }}>
            <div className="text-[11px] text-zinc-500">{t.label}</div>
            <div className="text-lg font-mono text-zinc-100">{fmtRate(t.sat_vb)} <span className="text-xs text-zinc-500">sat/vB</span></div>
            <div className="text-[11px] font-mono text-zinc-500">
              {t.usd != null ? `${fmtUSD(t.usd)} per ${live.standard_tx_vb} vB transfer` : `${live.standard_tx_vb} vB transfer`}
            </div>
          </div>
        ))}
      </div>
      <div className="text-xs text-zinc-400 font-mono">
        {fmtGas(live.tx_count)} unconfirmed · {live.vsize_mb.toFixed(1)} vMB ≈ {live.blocks_to_clear.toFixed(1)} blocks to clear ·{' '}
        {live.total_fee_btc.toFixed(4)} BTC in fees · min relay {fmtRate(live.minimum_sat_vb)} sat/vB
      </div>
      {total > 0 && (
        <div>
          <div className="flex h-3 rounded overflow-hidden">
            {live.bands.map((b, i) =>
              b.vsize_mb > 0 ? (
                <div key={b.label} title={`${b.label} sat/vB: ${b.vsize_mb.toFixed(2)} vMB`}
                  style={{ width: `${(b.vsize_mb / total) * 100}%`, background: BAND_COLORS[i] }} />
              ) : null,
            )}
          </div>
          <div className="flex flex-wrap gap-3 mt-2 text-[10px] font-mono text-zinc-500">
            {live.bands.map((b, i) => (
              <span key={b.label}>
                <span className="inline-block w-2 h-2 rounded-sm mr-1" style={{ background: BAND_COLORS[i] }} />
                {b.label} sat/vB {b.vsize_mb.toFixed(1)} vMB
              </span>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function TronLiveBody({ live, calibration }: { live: TronLive; calibration: GasCalibration | null }) {
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 gap-3">
        {live.costs.map(c => (
          <div key={c.label} className="rounded-xl border border-zinc-800/50 px-3 py-2" style={{ background: '#0c0c0f' }}>
            <div className="text-[11px] text-zinc-500">{c.label}</div>
            <div className="flex items-center gap-1.5 text-lg font-mono text-zinc-100">
              <TokenIcon symbol="TRX" size={14} />
              <span>
                {c.burn_trx.toFixed(2)} <span className="text-xs text-zinc-500">TRX</span>
                {c.burn_usd != null && <span className="text-xs text-zinc-500"> · {fmtUSD(c.burn_usd)}</span>}
              </span>
            </div>
            <div className="text-[11px] font-mono text-zinc-500">
              {c.energy.toLocaleString('en')} energy + {c.bandwidth} bandwidth · {c.share_pct.toFixed(0)}% of the two modal transfer types
            </div>
          </div>
        ))}
      </div>
      <div className="text-xs text-zinc-400 font-mono">
        energy {live.energy_price_sun} sun{live.energy_price_since ? ` since ${live.energy_price_since.slice(0, 10)}` : ''} ·
        bandwidth {live.bandwidth_price_sun} sun · {live.note}
      </div>
      {calibration && (
        <div className="text-[10px] text-zinc-600">
          Measured {calibration.measured_at.slice(0, 16).replace('T', ' ')} UTC · {calibration.windows}
        </div>
      )}
    </div>
  );
}

// mempool.space layout: projected blocks (+3 +2 +1) on the left, the newest
// mined blocks on the right, split by the "now" line.
function BtcConveyor({ live, asOf }: { live: BtcLive; asOf: string }) {
  if (live.projected.length === 0 && live.recent.length === 0) return null;
  const now = Date.parse(asOf);
  const projected = [...live.projected].reverse();
  return (
    <div className="flex items-stretch gap-2 overflow-x-auto pb-1">
      {projected.map((b, i) => (
        <div key={`p${i}`} className="shrink-0 w-32 rounded-lg border border-dashed border-zinc-600 px-2 py-1.5 font-mono text-[10px] text-zinc-400">
          <div className="text-zinc-300">+{projected.length - i} next</div>
          <div className="text-sm text-zinc-100">~{fmtRate(b.median_fee)} <span className="text-[10px] text-zinc-500">sat/vB</span></div>
          <div>{fmtRate(b.min_fee)} – {fmtRate(b.max_fee)}</div>
          <div>{b.vsize_mb.toFixed(2)} vMB · {fmtGas(b.tx_count)} tx</div>
        </div>
      ))}
      <div className="shrink-0 w-px bg-cyan-500/70 mx-1" title="now" />
      {live.recent.map(b => (
        <div key={b.height} className="shrink-0 w-32 rounded-lg border border-amber-500/40 px-2 py-1.5 font-mono text-[10px] text-zinc-400" style={{ background: 'rgba(245,158,11,0.08)' }}>
          <div className="text-amber-300">#{b.height}</div>
          <div className="text-sm text-zinc-100">{fmtRate(b.median_fee)} <span className="text-[10px] text-zinc-500">sat/vB</span></div>
          <div>{b.size_mb.toFixed(2)} MB · {b.fullness_pct.toFixed(1)}%</div>
          <div>{Math.max(0, Math.round((now - Date.parse(b.mined_at)) / 60000))}m ago · {b.total_fee_btc.toFixed(3)} BTC</div>
        </div>
      ))}
    </div>
  );
}

function L2LadderBody({ ladder, highlight }: { ladder: L2Ladder; highlight?: string }) {
  const labels = ladder.rows.find(r => r.actions.length > 0)?.actions ?? [];
  return (
    <div className="space-y-2">
      <table className="w-full text-xs font-mono">
        <thead>
          <tr className="text-zinc-500 text-left">
            <th className="py-1 font-normal">Network</th>
            <th className="py-1 font-normal">Gas price</th>
            {labels.map(a => (
              <th key={a.label} className="py-1 font-normal" title={a.source}>
                <span className="inline-flex items-center gap-1.5">
                  <TokenIcon symbol={a.label.toUpperCase().includes('USDC') ? 'USDC' : 'ETH'} size={13} />
                  <span>{a.label}</span>
                  <span className="text-zinc-600">({a.gas.toLocaleString('en')} gas)</span>
                </span>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {ladder.rows.map(r => (
            <tr key={r.id} className="border-t border-zinc-800/60" style={r.id === highlight ? { background: '#18181b' } : undefined}>
              <td className="py-1.5 text-zinc-200">
                <span className="inline-flex items-center gap-1.5">
                  <ChainIcon chain={r.id} size={14} />
                  <span>{r.name}</span>
                </span>
              </td>
              {r.error ? (
                <td colSpan={1 + labels.length} className="py-1.5 text-red-400/80">{r.error}</td>
              ) : (
                <>
                  <td className="py-1.5 text-zinc-400">{fmtGas(r.gas_price_gwei)} gwei</td>
                  {r.actions.map(a => (
                    <td key={a.label} className="py-1.5">
                      <div className="text-zinc-100">{a.total_usd != null ? fmtUSD(a.total_usd) : `${fmtGas(a.total_eth)} ETH`}</div>
                      <div className="text-[10px] text-zinc-500">
                        {a.savings_pct != null ? `${a.savings_pct.toFixed(1)}% cheaper than L1` : r.kind === 'l1' ? 'L1 reference' : ''}
                        {a.l1_share_pct > 0 ? ` · L1 data ${a.l1_share_pct.toFixed(1)}%` : ''}
                      </div>
                    </td>
                  ))}
                </>
              )}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="text-[10px] text-zinc-600">{ladder.note}</div>
    </div>
  );
}

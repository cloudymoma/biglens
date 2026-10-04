import { useEffect, useState } from 'react';
import type { BtcLive, GasLiveData, TronLive } from '../../types';
import { fetchGasLive } from '../../api';
import { ErrorBanner } from '../../dashboards/shared';
import { Panel } from './shared';
import { fmtGas } from './gasPulse';

const LIVE_POLL_MS = 30_000;

// Fee bands from cheap to expensive.
const BAND_COLORS = ['#34d399', '#a3e635', '#facc15', '#fb923c', '#f87171', '#e11d48'];

// Keep sub-1 sat/vB precision: 0.2 must never render as 0.
const fmtRate = (v: number): string => v.toLocaleString('en', { maximumFractionDigits: 3 });

const fmtUSD = (v: number | null): string =>
  v == null ? '' : `$${v < 0.01 ? v.toFixed(4) : v.toFixed(2)}`;

const fmtAsOf = (iso: string): string => `${iso.slice(11, 19)} UTC`;

export default function GasLiveBar({ chain }: { chain: 'btc' | 'tron' }) {
  const [data, setData] = useState<GasLiveData | null>(null);
  const [error, setError] = useState('');

  // Poll while mounted; the parent keys this component by chain, so switching
  // chains unmounts it and the cleanup stops the timer.
  useEffect(() => {
    let alive = true;
    const load = () =>
      fetchGasLive()
        .then(d => {
          if (!alive) return;
          setData(d);
          setError('');
        })
        .catch(e => {
          if (alive) setError(e.response?.data || e.message);
        });
    load();
    const id = setInterval(load, LIVE_POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, []);

  if (error) return <ErrorBanner message={error} />;
  if (!data) return <div className="text-[11px] text-zinc-600">Loading live data…</div>;

  const note = `updated ${fmtAsOf(data.as_of)} · refreshes every 30s`;
  if (chain === 'btc') {
    return (
      <Panel title="Bitcoin live · mempool.space" note={note}>
        {data.btc ? <BtcLiveBody live={data.btc} /> : <ErrorBanner message={data.btc_error || 'unavailable'} />}
      </Panel>
    );
  }
  return (
    <Panel title="TRON live · USDT transfer burn cost" note={note}>
      {data.tron ? <TronLiveBody live={data.tron} /> : <ErrorBanner message={data.tron_error || 'unavailable'} />}
    </Panel>
  );
}

function BtcLiveBody({ live }: { live: BtcLive }) {
  const total = live.bands.reduce((s, b) => s + b.vsize_mb, 0);
  return (
    <div className="space-y-4">
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

function TronLiveBody({ live }: { live: TronLive }) {
  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 gap-3">
        {live.costs.map(c => (
          <div key={c.label} className="rounded-xl border border-zinc-800/50 px-3 py-2" style={{ background: '#0c0c0f' }}>
            <div className="text-[11px] text-zinc-500">{c.label}</div>
            <div className="text-lg font-mono text-zinc-100">
              {c.burn_trx.toFixed(2)} <span className="text-xs text-zinc-500">TRX</span>
              {c.burn_usd != null && <span className="text-xs text-zinc-500"> · {fmtUSD(c.burn_usd)}</span>}
            </div>
            <div className="text-[11px] font-mono text-zinc-500">
              {c.energy.toLocaleString('en')} energy + ≈{c.bandwidth} bandwidth
            </div>
          </div>
        ))}
      </div>
      <div className="text-xs text-zinc-400 font-mono">
        energy {live.energy_price_sun} sun{live.energy_price_since ? ` since ${live.energy_price_since.slice(0, 10)}` : ''} ·
        bandwidth {live.bandwidth_price_sun} sun · {live.note}
      </div>
    </div>
  );
}

import { Bell, Layers } from 'lucide-react';
import { fmtPct } from '../common/format';
import { fmtBps, fmtMoney, fmtNative, fmtPrice, fmtRangeWindow, fmtTimeHM } from './format';
import SignalChip from './SignalChip';
import { cx } from './styles';
import type { StableLive, StableLiveAsset } from './types';

// Live strip (stablecoin.md §7.1): per core asset the consensus price (EURC
// in €), a deviation chip colored by S1, the 24h deviation range, issuer
// circulating, 24h net issuance and the worst signal; then the total
// stablecoin supply with its 30-day change, and the alert counts. Served from
// BigLens' latest samples; polled every 60 s while the browser tab is visible.

function AssetCell({ a, pegWarmingUp }: { a: StableLiveAsset; pegWarmingUp: boolean }) {
  const noConsensus = !pegWarmingUp && a.consensus === null;
  return (
    <div className={cx.cardTight}>
      <div className="flex items-center justify-between gap-2">
        <span className={`text-xs font-semibold ${cx.strong}`}>
          {a.symbol} <span className={`font-normal ${cx.muted}`}>in {a.numeraire === 'EUR' ? '€' : '$'}</span>
        </span>
        <SignalChip state={a.worst_signal} prefix="worst" title="Worst signal state across S1–S13 (info counts as normal)" />
      </div>
      <div className={`mt-1 font-mono text-lg font-bold ${cx.strong}`}>{fmtPrice(a.consensus, a.numeraire)}</div>
      <div className="mt-1 flex flex-wrap items-center gap-1.5">
        <SignalChip state={a.peg_signal} value={fmtBps(a.dev_bps)} title="Deviation from par; the chip state is S1 (peg)" />
        {a.venues_used !== null && <span className={`text-[11px] ${cx.muted}`}>{a.venues_used} venues</span>}
      </div>
      {pegWarmingUp && <p className={`mt-1 text-[11px] ${cx.muted}`}>Waiting for first sample</p>}
      {noConsensus && (
        <p className={`mt-1 text-[11px] ${cx.muted}`}>No consensus: fewer than 2 distinct venues ({a.peg_state || 'unknown'})</p>
      )}
      <dl className="mt-2 space-y-0.5 text-[11px]">
        <div className="flex justify-between gap-2">
          <dt className={cx.muted}>24h range ({fmtRangeWindow(a.dev_range_from, a.dev_range_partial)})</dt>
          <dd className={`font-mono ${cx.text}`}>
            {a.dev_min_24h_bps === null ? '—' : `${fmtBps(a.dev_min_24h_bps)} … ${fmtBps(a.dev_max_24h_bps)}`}
          </dd>
        </div>
        <div className="flex justify-between gap-2">
          <dt className={cx.muted}>Circulating{a.circulating_source ? ` (${a.circulating_source})` : ''}</dt>
          <dd className={`font-mono ${cx.text}`}>
            {fmtNative(a.circulating_native, a.symbol)}
            {a.circulating_stale && ' · stale'}
            {a.circulating_degraded && ' · degraded'}
          </dd>
        </div>
        <div className="flex justify-between gap-2">
          <dt className={cx.muted}>24h net issuance{a.net_issuance_24h_source ? ` (${a.net_issuance_24h_source})` : ''}</dt>
          <dd className={`font-mono ${cx.text}`}>
            {a.net_issuance_24h_native === null
              ? '—'
              : `${a.net_issuance_24h_native > 0 ? '+' : ''}${fmtNative(a.net_issuance_24h_native, a.symbol)} (${fmtPct(a.net_issuance_24h_pct, 2, true)})`}
          </dd>
        </div>
      </dl>
      <p className={`mt-2 text-[10px] ${cx.faint}`}>
        price as of {fmtTimeHM(a.price_as_of)}
        {a.price_stale && ' · stale (sampler missed 3 intervals)'}
      </p>
    </div>
  );
}

export default function StablecoinTicker({ live, loading }: { live: StableLive | null; loading: boolean }) {
  if (!live) {
    return (
      <div className="grid grid-cols-1 gap-3 md:grid-cols-3 xl:grid-cols-5" aria-busy={loading}>
        {Array.from({ length: 5 }).map((_, i) => (
          <div key={i} className={`h-40 animate-pulse motion-reduce:animate-none ${cx.card}`} />
        ))}
      </div>
    );
  }
  const alerts = live.alerts;
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-3 xl:grid-cols-5" aria-label="Live stablecoin ticker">
      {live.assets.map(a => (
        <AssetCell key={a.asset} a={a} pegWarmingUp={live.peg_warming_up} />
      ))}
      <div className={cx.cardTight}>
        <div className="flex items-center justify-between">
          <span className={`text-xs font-semibold ${cx.strong}`}>All stablecoins</span>
          <Layers className={`h-3.5 w-3.5 ${cx.muted}`} aria-hidden="true" />
        </div>
        <div className={`mt-1 font-mono text-lg font-bold ${cx.strong}`}>{fmtMoney(live.market_total_usd, 'USD')}</div>
        <div className={`mt-1 text-[11px] ${cx.muted}`}>
          30d change <span className={`font-mono ${cx.text}`}>{fmtPct(live.market_total_30d_change_pct, 2, true)}</span>
        </div>
        <p className={`mt-2 text-[10px] ${cx.faint}`}>DefiLlama, all peg types · as of {fmtTimeHM(live.market_as_of)}</p>
      </div>
      <div className={cx.cardTight}>
        <div className="flex items-center justify-between">
          <span className={`text-xs font-semibold ${cx.strong}`}>Alerts</span>
          <Bell className={`h-3.5 w-3.5 ${cx.muted}`} aria-hidden="true" />
        </div>
        {alerts ? (
          <div className="mt-2 flex flex-col items-start gap-1.5">
            <SignalChip state="critical" value={String(alerts.critical ?? 0)} />
            <SignalChip state="warning" value={String(alerts.warning ?? 0)} />
            <SignalChip state="watch" value={String(alerts.watch ?? 0)} />
          </div>
        ) : (
          <p className={`mt-2 text-[11px] ${cx.muted}`}>Signal engine not running</p>
        )}
        <p className={`mt-2 text-[10px] ${cx.faint}`}>signals across USDT, USDC and EURC</p>
      </div>
    </div>
  );
}

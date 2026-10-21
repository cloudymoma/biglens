import { Activity, Blocks, Cpu, Flame, Gauge, Layers, TrendingDown, TrendingUp } from 'lucide-react';
import type { BtcLiveData } from './types';
import { fmtPct, fmtRelativeAgo, fmtUSD } from '../common/format';
import { fmtSatVB } from './format';

export default function BitcoinTicker({
  live,
  loading,
}: {
  live: BtcLiveData | null;
  loading?: boolean;
}) {
  if (!live && loading) {
    return (
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4 xl:grid-cols-7">
        {Array.from({ length: 7 }).map((_, i) => (
          <div
            key={i}
            className="h-20 animate-pulse rounded-xl border border-zinc-800/80 bg-zinc-900/50 p-3"
          />
        ))}
      </div>
    );
  }

  if (!live) return null;

  const changeUp = (live.change_24h_pct ?? 0) >= 0;
  const diff = live.difficulty;
  const halving = live.halving;

  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-4 xl:grid-cols-7">
      {/* 1. Spot Price */}
      <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3">
        <div className="flex items-center justify-between text-[11px] text-zinc-400">
          <span>BTC Spot (USD)</span>
          {changeUp ? (
            <TrendingUp className="h-3.5 w-3.5 text-emerald-400" />
          ) : (
            <TrendingDown className="h-3.5 w-3.5 text-red-400" />
          )}
        </div>
        <div className="mt-1 text-lg font-bold tracking-tight text-zinc-100">
          {live.price_usd !== null ? fmtUSD(live.price_usd, 0) : '—'}
        </div>
        <div
          className={`mt-0.5 text-[11px] font-medium ${
            live.change_24h_pct === null
              ? 'text-zinc-500'
              : changeUp
                ? 'text-emerald-400'
                : 'text-red-400'
          }`}
        >
          {live.change_24h_pct !== null ? `${fmtPct(live.change_24h_pct, 2, true)} (24h)` : '24h n/a'}
        </div>
      </div>

      {/* 2. Tip Height */}
      <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3">
        <div className="flex items-center justify-between text-[11px] text-zinc-400">
          <span>Block Height</span>
          <Blocks className="h-3.5 w-3.5 text-amber-400" />
        </div>
        <div className="mt-1 font-mono text-lg font-bold text-zinc-100">
          {live.tip_height != null && live.tip_height > 0 ? `#${live.tip_height.toLocaleString()}` : '—'}
        </div>
        <div className="mt-0.5 text-[11px] text-zinc-400">
          {live.tip_time ? `Mined ${fmtRelativeAgo(live.tip_time)}` : 'Awaiting tip'}
        </div>
      </div>

      {/* 3. Next-block fee */}
      <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3">
        <div className="flex items-center justify-between text-[11px] text-zinc-400">
          <span>Next-Block Fee</span>
          <Flame className="h-3.5 w-3.5 text-orange-400" />
        </div>
        <div className="mt-1 font-mono text-lg font-bold text-zinc-100">
          {live.next_block_sat_vb > 0 ? fmtSatVB(live.next_block_sat_vb) : '—'}
        </div>
        <div className="mt-0.5 text-[11px] text-zinc-400">
          {live.next_block_usd !== null
            ? `~${fmtUSD(live.next_block_usd, 2)} (141 vB P2WPKH)`
            : '141 vB P2WPKH'}
        </div>
      </div>

      {/* 4. Mempool Backlog */}
      <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3">
        <div className="flex items-center justify-between text-[11px] text-zinc-400">
          <span>Mempool Backlog</span>
          <Layers className="h-3.5 w-3.5 text-sky-400" />
        </div>
        <div className="mt-1 font-mono text-lg font-bold text-zinc-100">
          {live.tip_height != null ? `${live.mempool_vmb.toFixed(1)} vMB` : '—'}
        </div>
        <div className="mt-0.5 text-[11px] text-zinc-400">
          {live.tip_height != null ? `~${live.blocks_to_clear.toFixed(1)} blocks to clear` : '—'}
        </div>
      </div>

      {/* 5. Difficulty Retarget */}
      <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3">
        <div className="flex items-center justify-between text-[11px] text-zinc-400">
          <span>Difficulty Retarget</span>
          <Gauge className="h-3.5 w-3.5 text-purple-400" />
        </div>
        <div className="mt-1 font-mono text-lg font-bold text-zinc-100">
          {diff ? fmtPct(diff.est_change_pct, 2, true) : '—'}
        </div>
        <div className="mt-0.5 text-[11px] text-zinc-400">
          {diff
            ? `${diff.remaining_blocks.toLocaleString()} blks (${diff.progress_pct.toFixed(0)}%)`
            : 'Retarget n/a'}
        </div>
      </div>

      {/* 6. Halving Countdown */}
      <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3">
        <div className="flex items-center justify-between text-[11px] text-zinc-400">
          <span>
            {halving.subsidy_now_btc != null && halving.subsidy_next_btc != null
              ? `Halving (${halving.subsidy_now_btc} → ${halving.subsidy_next_btc})`
              : 'Halving'}
          </span>
          <Activity className="h-3.5 w-3.5 text-amber-400" />
        </div>
        <div className="mt-1 font-mono text-lg font-bold text-zinc-100">
          {halving.blocks_remaining != null && halving.blocks_remaining > 0
            ? `${halving.blocks_remaining.toLocaleString()} blks`
            : '—'}
        </div>
        <div className="mt-0.5 text-[11px] text-zinc-400">
          {halving.pct_of_21m != null ? `${halving.pct_of_21m.toFixed(2)}% of 21M issued` : '—'}
        </div>
      </div>

      {/* 7. Implied Hashrate */}
      <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/60 p-3">
        <div className="flex items-center justify-between text-[11px] text-zinc-400">
          <span>Hashrate (7d est.)</span>
          <Cpu className="h-3.5 w-3.5 text-emerald-400" />
        </div>
        <div className="mt-1 font-mono text-lg font-bold text-zinc-100">
          {live.hashrate_7d_ehs !== null ? `${live.hashrate_7d_ehs.toFixed(0)} EH/s` : '—'}
        </div>
        <div className="mt-0.5 text-[11px] text-zinc-400">
          {diff ? `Avg block ${(diff.avg_block_seconds / 60).toFixed(1)}m` : '144-block window'}
        </div>
      </div>
    </div>
  );
}

import { useEffect, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { Activity, ArrowDownUp, Sigma, Trophy } from 'lucide-react';
import type { GasPulseChain, GasPulseData } from '../../types';
import { fetchGasPulse } from '../../api';
import { MetricCard, EmptyState, ErrorBanner } from '../../dashboards/shared';
import { Panel } from './shared';
import {
  GAS_CHAIN_COLORS, TONE_COLOR, buildGasPulseOption, fmtAllTime, fmtGas, fmtWhen,
  percentileTone, shortHour,
} from './gasPulse';

export default function GasPulse72hView() {
  const [data, setData] = useState<GasPulseData | null>(null);
  const [error, setError] = useState('');
  const [selected, setSelected] = useState('btc');

  // No parameters, so fetch once; state is only set in the callbacks.
  useEffect(() => {
    fetchGasPulse()
      .then(setData)
      .catch(e => setError(e.response?.data || e.message));
  }, []);

  if (error) return <ErrorBanner message={error} />;
  if (!data) return <EmptyState text="Loading 72h gas pulse…" />;

  const chain = data.chains.find(c => c.meta.id === selected) ?? data.chains[0];
  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <span className="text-[11px] text-zinc-600">
          {data.window_start} → {data.window_end} UTC · complete hours · cached 1h
        </span>
      </div>
      <ChainStrip chains={data.chains} selected={chain.meta.id} onSelect={setSelected} />
      {chain.error ? (
        <ErrorBanner message={chain.error} />
      ) : chain.stats.samples === 0 ? (
        <EmptyState text={`No ${chain.meta.name} data in the last 72 complete hours.`} />
      ) : (
        <>
          <StatCards chain={chain} />
          <Panel title={`${chain.meta.name} · ${chain.meta.primary_label}`} note={chain.meta.note}>
            <ReactECharts option={buildGasPulseOption(chain)} style={{ height: 380 }} notMerge />
          </Panel>
        </>
      )}
    </div>
  );
}

function ChainStrip({ chains, selected, onSelect }: {
  chains: GasPulseChain[]; selected: string; onSelect: (id: string) => void;
}) {
  return (
    <div className="grid grid-cols-2 md:grid-cols-4 xl:grid-cols-7 gap-2">
      {chains.map(c => {
        const active = c.meta.id === selected;
        const ok = !c.error && c.stats.samples > 0;
        return (
          <button
            key={c.meta.id}
            onClick={() => onSelect(c.meta.id)}
            className={`text-left rounded-xl border px-3 py-2 cursor-pointer transition-colors ${
              active ? 'border-zinc-600' : 'border-zinc-800/50 hover:border-zinc-700'
            }`}
            style={{ background: active ? '#18181b' : '#111114' }}
          >
            <div className="text-[11px] font-medium" style={{ color: GAS_CHAIN_COLORS[c.meta.id] }}>{c.meta.name}</div>
            <div className="text-sm text-zinc-200 font-mono mt-0.5 truncate">
              {ok ? `${fmtGas(c.stats.latest)} ${c.meta.primary_unit}` : '—'}
            </div>
            <div className="text-[10px] font-mono mt-0.5"
              style={{ color: ok ? TONE_COLOR[percentileTone(c.stats.percentile)] : '#52525b' }}>
              {c.error ? 'unavailable' : ok ? `P${c.stats.percentile} · 72h` : 'no data'}
            </div>
          </button>
        );
      })}
    </div>
  );
}

function StatCards({ chain }: { chain: GasPulseChain }) {
  const { meta, stats: s } = chain;
  const color = GAS_CHAIN_COLORS[meta.id];
  const unit = meta.primary_unit;
  return (
    <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
      <MetricCard
        label="Latest Complete Hour"
        value={`${fmtGas(s.latest)} ${unit}`}
        icon={<Activity size={15} />}
        detail={`P${s.percentile} of 72h · ${shortHour(s.latest_hour)} UTC`}
        accentColor={TONE_COLOR[percentileTone(s.percentile)]}
      />
      <MetricCard
        label="72h Low ↔ High"
        value={`${fmtGas(s.min_value)} ↔ ${fmtGas(s.max_value)}`}
        icon={<ArrowDownUp size={15} />}
        detail={`low ${shortHour(s.min_hour)} · high ${shortHour(s.max_hour)} UTC`}
        accentColor={color}
      />
      <MetricCard
        label="72h Median · P90"
        value={`${fmtGas(s.median)} · ${fmtGas(s.p90)}`}
        icon={<Sigma size={15} />}
        detail={`72h total ${fmtGas(s.total_fee)} ${meta.fee_unit}`}
        accentColor={color}
      />
      <AllTimeCard chain={chain} />
    </div>
  );
}

function AllTimeCard({ chain }: { chain: GasPulseChain }) {
  const { meta, stats: s, all_time: at, all_time_error: err } = chain;
  const color = GAS_CHAIN_COLORS[meta.id];
  if (err) {
    return <MetricCard label="All-Time Record" value="—" icon={<Trophy size={15} />} detail={err} accentColor={color} />;
  }
  if (!at) {
    // BTC and Solana have no cheap full-history fee series (gas_fee_design.md §4.1).
    return (
      <MetricCard
        label="72h Peak Load"
        value={`${fmtGas(s.load_max)} ${meta.load_unit}`}
        icon={<Trophy size={15} />}
        detail={`${shortHour(s.load_max_hour)} UTC · 72h ${fmtGas(s.tx_count)} tx`}
        accentColor={color}
      />
    );
  }
  const atl = at.atl_is_floor
    ? `ATL ${fmtAllTime(at.atl_value, at.unit)} protocol floor since ${at.atl_time.slice(0, 10)}`
    : `ATL ${fmtAllTime(at.atl_value, at.unit)} ${fmtWhen(at.atl_time)}`;
  const now = at.current_value != null ? ` · now ${fmtAllTime(at.current_value, at.unit)}` : '';
  return (
    <MetricCard
      label={meta.id === 'tron' ? 'Energy Price Record' : 'All-Time Base Fee'}
      value={`ATH ${fmtAllTime(at.ath_value, at.unit)}`}
      icon={<Trophy size={15} />}
      detail={`${fmtWhen(at.ath_time)} · ${atl}${now}`}
      accentColor={color}
    />
  );
}

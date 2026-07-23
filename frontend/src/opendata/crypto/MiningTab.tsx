import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { Cpu, Pickaxe, Zap, Coins } from 'lucide-react';
import type { BtcMiningRow, CryptoMiningData } from '../../types';
import { fetchCryptoMining, fetchCryptoSpot } from '../../api';
import { MetricCard, EmptyState, ErrorBanner } from '../../dashboards/shared';
import {
  BTC_COLOR, CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE,
  fmtNum, Panel, DaysPicker,
} from './shared';

const DAY_OPTIONS = [7, 30, 90, 365];

// Representative ASIC generations, oldest to newest. Efficiency in J/TH.
const RIGS = [
  { name: 'Antminer S9 (2016)', jth: 98, color: '#a1a1aa' },
  { name: 'Antminer S19 Pro (2020)', jth: 29.5, color: '#60a5fa' },
  { name: 'Antminer S19 XP (2022)', jth: 21.5, color: '#a78bfa' },
  { name: 'Antminer S21 (2023)', jth: 17.5, color: BTC_COLOR },
  { name: 'Antminer S21 XP (2024)', jth: 13.5, color: '#22c55e' },
];

// A sustained 1 TH/s burns e J/TH × 86400 TH/day = 0.024·e kWh/day.
const KWH_PER_JTH_DAY = 0.024;

// BTC earned per day by 1 TH/s at network-average luck (1 EH/s = 1e6 TH/s).
function btcPerThDay(r: BtcMiningRow): number {
  return r.hashrate_ehs > 0 ? r.revenue_btc / (r.hashrate_ehs * 1e6) : 0;
}

// Electricity price ($/kWh) at which mining revenue exactly covers power.
function breakEvenKwh(r: BtcMiningRow, jth: number, price: number): number {
  return (btcPerThDay(r) * price) / (KWH_PER_JTH_DAY * jth);
}

// Electricity cost ($) to mine one whole BTC with a rig of efficiency jth.
function costPerBtc(r: BtcMiningRow, jth: number, elec: number): number {
  const perDay = btcPerThDay(r);
  return perDay > 0 ? ((KWH_PER_JTH_DAY * jth) / perDay) * elec : 0;
}

function NumberInput({ label, unit, value, onChange, width = 'w-28' }: {
  label: string; unit: string; value: string; onChange: (v: string) => void; width?: string;
}) {
  return (
    <label className="flex items-center gap-2 text-xs text-zinc-400">
      {label}
      <input
        type="number"
        min="0"
        step="any"
        value={value}
        onChange={e => onChange(e.target.value)}
        className={`${width} bg-zinc-900 border border-zinc-800 rounded-lg px-3 py-1.5 text-xs text-white focus:outline-none focus:border-zinc-600`}
      />
      <span className="text-zinc-600">{unit}</span>
    </label>
  );
}

export default function MiningTab() {
  const [days, setDays] = useState(90);
  const [data, setData] = useState<CryptoMiningData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [priceStr, setPriceStr] = useState('');
  const [spotNote, setSpotNote] = useState('spot unavailable — enter a price');
  const [elecStr, setElecStr] = useState('0.06');
  const [customJthStr, setCustomJthStr] = useState('18');

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchCryptoMining(days)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [days]);

  useEffect(() => {
    fetchCryptoSpot().then(spot => {
      if (!spot) return;
      setSpotNote(`Coinbase spot · ${spot.as_of.slice(0, 16).replace('T', ' ')} UTC`);
      // Only prefill if the user hasn't typed a price yet.
      setPriceStr(prev => (prev === '' ? String(spot.price_usd) : prev));
    });
  }, []);

  if (error) return <div className="space-y-4"><DaysPicker options={DAY_OPTIONS} value={days} onChange={setDays} /><ErrorBanner message={error} /></div>;
  if (loading || !data) return <EmptyState text="Loading mining economics…" />;

  const latest = data.daily[data.daily.length - 1];
  if (!latest) return <EmptyState text="No mining data in this window" />;

  const price = Math.max(0, parseFloat(priceStr) || 0);
  const elec = Math.max(0, parseFloat(elecStr) || 0);
  const customJth = Math.max(0, parseFloat(customJthStr) || 0);
  const dates = data.daily.map(r => r.date);
  const newestRig = RIGS[RIGS.length - 1];
  const satsPerThDay = btcPerThDay(latest) * 1e8;

  const tableRigs = [
    ...RIGS,
    ...(customJth > 0 ? [{ name: 'Custom rig', jth: customJth, color: '#ef4444' }] : []),
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <DaysPicker options={DAY_OPTIONS} value={days} onChange={setDays} />
        <span className="text-[11px] text-zinc-600">Complete UTC days · latest = yesterday</span>
      </div>

      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <MetricCard label="Network Hashrate" value={`${fmtNum(latest.hashrate_ehs)} EH/s`} icon={<Cpu size={15} />} detail={`${latest.blocks} blocks · ${latest.date}`} accentColor={BTC_COLOR} />
        <MetricCard label="Miner Revenue" value={`${fmtNum(latest.revenue_btc)} BTC`} icon={<Pickaxe size={15} />} detail={price > 0 ? `≈ $${fmtNum(latest.revenue_btc * price)}/day` : 'subsidy + fees'} accentColor={BTC_COLOR} />
        <MetricCard label="Yield per TH/s" value={`${satsPerThDay.toFixed(1)} sats/day`} icon={<Coins size={15} />} detail="network-average luck" accentColor={BTC_COLOR} />
        <MetricCard label={`Break-even (${newestRig.jth} J/TH)`} value={price > 0 ? `$${breakEvenKwh(latest, newestRig.jth, price).toFixed(3)}/kWh` : '—'} icon={<Zap size={15} />} detail={price > 0 ? `at $${fmtNum(price)}/BTC` : 'enter a BTC price'} accentColor="#22c55e" />
      </div>

      <Panel title="Assumptions" note={spotNote}>
        <div className="flex flex-wrap items-center gap-6">
          <NumberInput label="BTC price" unit="USD" value={priceStr} onChange={setPriceStr} />
          <NumberInput label="Your electricity" unit="$/kWh" value={elecStr} onChange={setElecStr} width="w-20" />
          <NumberInput label="Custom rig" unit="J/TH" value={customJthStr} onChange={setCustomJthStr} width="w-20" />
        </div>
      </Panel>

      <Panel title="Break-even Electricity Price by Rig" note="max $/kWh at which each rig still profits · dashed line = your electricity price">
        <ReactECharts
          style={{ height: 300 }}
          option={{
            tooltip: { trigger: 'axis', ...CHART_TOOLTIP, valueFormatter: (v: number) => `$${(v ?? 0).toFixed(3)}/kWh` },
            legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
            grid: { left: 60, right: 80, top: 32, bottom: 24 },
            xAxis: { type: 'category', data: dates, axisLabel: AXIS_LABEL },
            yAxis: { type: 'value', axisLabel: { ...AXIS_LABEL, formatter: '${value}' }, splitLine: SPLIT_LINE },
            series: RIGS.map((rig, i) => ({
              name: `${rig.jth} J/TH`,
              type: 'line',
              showSymbol: false,
              data: data.daily.map(r => Number(breakEvenKwh(r, rig.jth, price).toFixed(4))),
              lineStyle: { color: rig.color, width: 2 },
              itemStyle: { color: rig.color },
              ...(i === 0 && elec > 0 ? {
                markLine: {
                  silent: true,
                  symbol: 'none',
                  lineStyle: { color: '#ef4444', type: 'dashed', width: 1.5 },
                  label: { color: '#ef4444', fontSize: 10, formatter: `your power $${elec}/kWh` },
                  data: [{ yAxis: elec }],
                },
              } : {}),
            })),
          }}
        />
      </Panel>

      <Panel title="Rig Economics (latest day)" note={`electricity at $${elec}/kWh · BTC at $${fmtNum(price)}`}>
        <table className="w-full text-xs">
          <thead>
            <tr className="text-zinc-500 border-b border-zinc-800/60">
              <th className="text-left py-2 font-medium">Rig</th>
              <th className="text-right py-2 font-medium">Efficiency</th>
              <th className="text-right py-2 font-medium">Break-even</th>
              <th className="text-right py-2 font-medium">Cost to mine 1 BTC</th>
              <th className="text-right py-2 font-medium">Margin vs price</th>
            </tr>
          </thead>
          <tbody>
            {tableRigs.map(rig => {
              const be = breakEvenKwh(latest, rig.jth, price);
              const cost = costPerBtc(latest, rig.jth, elec);
              const margin = price > 0 && cost > 0 ? ((price - cost) / price) * 100 : 0;
              const profitable = price > 0 && be >= elec;
              return (
                <tr key={rig.name} className="border-b border-zinc-800/30">
                  <td className="py-2 text-zinc-300">
                    <span className="inline-block w-2 h-2 rounded-full mr-2" style={{ background: rig.color }} />
                    {rig.name}
                  </td>
                  <td className="py-2 text-right text-zinc-400 font-mono">{rig.jth} J/TH</td>
                  <td className="py-2 text-right font-mono" style={{ color: profitable ? '#22c55e' : '#ef4444' }}>
                    {price > 0 ? `$${be.toFixed(3)}/kWh` : '—'}
                  </td>
                  <td className="py-2 text-right text-zinc-300 font-mono">{cost > 0 ? `$${fmtNum(cost)}` : '—'}</td>
                  <td className="py-2 text-right font-mono" style={{ color: margin >= 0 ? '#22c55e' : '#ef4444' }}>
                    {price > 0 && cost > 0 ? `${margin >= 0 ? '+' : ''}${margin.toFixed(1)}%` : '—'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Panel>

      <p className="text-[11px] text-zinc-600">
        Electricity-only model: excludes hardware capex, cooling, pool fees (~1–2%) and assumes
        network-average luck. Hashrate is implied from difficulty (decoded from block header bits)
        and actual daily block count; revenue is total coinbase output (subsidy + fees).
      </p>
    </div>
  );
}

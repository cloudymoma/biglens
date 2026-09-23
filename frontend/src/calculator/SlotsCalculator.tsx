import { useMemo, useRef, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { Plus, Trash2, Clock, CalendarDays, Activity, Gauge } from 'lucide-react';
import { MetricCard } from '../dashboards/shared';
import { postSlotsCalculation } from '../api';
import type { CalculatorPresets, PeakWindow, SlotsCalcRequest } from '../types';
import { useCalculation } from './useCalculation';
import { Card, GroupLabel, NumField, SelectField, ResetButton, CalcStatus } from './shared';
import { fmtUSD, fmtNum, CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE, SERIES_COLORS } from './format';

const TERM_OPTIONS = [{ value: '1yr', label: '1 year' }, { value: '3yr', label: '3 years' }];

const COMMITTED_COLOR = '#4ade80';
const BASELINE_PAYG_COLOR = '#fbbf24';
const AUTOSCALED_COLOR = '#c084fc';

const BUCKETS_PER_DAY = 96;
const BUCKET_HOURS = 24 / BUCKETS_PER_DAY;
// 97 labels: the profile is repeated at 24:00 so the last step closes the day.
const BUCKET_LABELS = Array.from({ length: BUCKETS_PER_DAY + 1 }, (_, i) => {
  const h = Math.floor(i * BUCKET_HOURS);
  const m = Math.round((i * BUCKET_HOURS - h) * 60);
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`;
});
const HOUR_LABELS = Array.from({ length: 24 }, (_, h) => `${String(h).padStart(2, '0')}:00`);

export default function SlotsCalculator({ presets }: { presets: CalculatorPresets }) {
  const editions = presets.editions;
  const step = presets.slot_increment;
  const listCommitRate = (e: string, t: string) => editions.find(x => x.edition === e)?.commit[t] ?? 0;

  const [edition, setEdition] = useState('enterprise');
  const [term, setTerm] = useState('1yr');
  const [baseline, setBaseline] = useState(1000);
  const [windows, setWindows] = useState<PeakWindow[]>([{ id: 'w1', start: '02:00', end: '03:30', slots: 4000 }]);
  const [committed, setCommitted] = useState(1000);
  const [paygRate, setPaygRate] = useState<number>(editions.find(e => e.edition === 'enterprise')?.payg ?? 0);
  const [commitRate, setCommitRate] = useState<number>(listCommitRate('enterprise', '1yr'));
  const [discount, setDiscount] = useState(0);
  const nextId = useRef(2);

  const ed = editions.find(e => e.edition === edition) ?? editions[0];
  const canCommit = Object.keys(ed.commit).length > 0;
  const canBaseline = ed.baseline;

  const resetRates = (e: string, t: string) => {
    setPaygRate(editions.find(x => x.edition === e)?.payg ?? 0);
    setCommitRate(listCommitRate(e, t));
  };
  const changeEdition = (e: string) => { setEdition(e); resetRates(e, term); };
  const changeTerm = (t: string) => { setTerm(t); resetRates(edition, t); };

  const addWindow = () => setWindows(ws => [...ws, { id: `w${nextId.current++}`, start: '09:00', end: '12:00', slots: 2000 }]);
  const updateWindow = (id: string, patch: Partial<PeakWindow>) =>
    setWindows(ws => ws.map(w => (w.id === id ? { ...w, ...patch } : w)));
  const removeWindow = (id: string) => setWindows(ws => ws.filter(w => w.id !== id));

  // The backend applies the edition rules (Standard: no baseline, no
  // commitment, 1,600 cap) and reports them back as warnings.
  const req = useMemo<SlotsCalcRequest>(() => ({
    edition, baseline_slots: baseline, committed_slots: committed, windows,
    payg_rate: paygRate, commit_rate: commitRate, discount_pct: discount,
  }), [edition, baseline, committed, windows, paygRate, commitRate, discount]);
  const { est, loading, error } = useCalculation(req, postSlotsCalculation);

  const committedNow = est?.effective_committed ?? 0;
  const best = est?.best_commit;
  const paygShare = est && est.monthly.total > 0 ? (est.monthly.payg / est.monthly.total) * 100 : 0;

  const usageOption = est && {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP, valueFormatter: (v: number) => `${fmtNum(v)} slots` },
    legend: { bottom: 0, textStyle: { color: '#71717a', fontSize: 11 }, itemWidth: 10, itemHeight: 10 },
    grid: { left: 8, right: 16, top: 24, bottom: 36, containLabel: true },
    xAxis: { type: 'category', data: BUCKET_LABELS, boundaryGap: false,
      axisLabel: { ...AXIS_LABEL, interval: 11 }, axisLine: { lineStyle: { color: '#27272a' } } },
    yAxis: { type: 'value', axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtNum(v) }, splitLine: SPLIT_LINE },
    series: [
      area('Baseline', est.profile.baseline, SERIES_COLORS[0]),
      ...est.profile.windows.map((series, i) => area(
        windows[i] ? `${windows[i].start}–${windows[i].end}` : `Window ${i + 1}`,
        series, SERIES_COLORS[(i + 1) % SERIES_COLORS.length],
      )),
      {
        name: 'Committed',
        type: 'line',
        data: committedNow > 0 ? BUCKET_LABELS.map(() => committedNow) : [],
        showSymbol: false,
        silent: true,
        tooltip: { show: false },
        lineStyle: { color: '#e4e4e7', type: 'dashed', width: 1.5 },
        itemStyle: { color: '#e4e4e7' },
        markLine: committedNow > 0 ? {
          symbol: 'none', silent: true,
          lineStyle: { opacity: 0 },
          label: { color: '#e4e4e7', fontSize: 10, formatter: `committed ${fmtNum(committedNow)}`, position: 'insideEndTop' },
          data: [{ yAxis: committedNow }],
        } : undefined,
      },
    ],
  };

  const costOption = est && {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, ...CHART_TOOLTIP, valueFormatter: (v: number) => fmtUSD(v) },
    legend: { bottom: 0, textStyle: { color: '#71717a', fontSize: 11 }, itemWidth: 10, itemHeight: 10 },
    grid: { left: 8, right: 16, top: 12, bottom: 36, containLabel: true },
    xAxis: { type: 'category', data: HOUR_LABELS, axisLabel: { ...AXIS_LABEL, interval: 2 }, axisLine: { lineStyle: { color: '#27272a' } } },
    yAxis: { type: 'value', axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtUSD(v, 0) }, splitLine: SPLIT_LINE },
    series: [
      costBar('Committed', est.hourly_cost.map(h => h.committed), COMMITTED_COLOR),
      costBar('Baseline (PAYG)', est.hourly_cost.map(h => h.uncommitted_baseline), BASELINE_PAYG_COLOR),
      costBar('Autoscaled (PAYG)', est.hourly_cost.map(h => h.autoscaled), AUTOSCALED_COLOR),
    ],
  };

  const sweepOption = est && {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP,
      formatter: (ps: { data: [number, number] }[]) => `Commit ${fmtNum(ps[0].data[0])} slots<br/>${fmtUSD(ps[0].data[1], 0)} / month` },
    grid: { left: 8, right: 24, top: 24, bottom: 28, containLabel: true },
    xAxis: { type: 'value', name: 'committed slots', nameLocation: 'middle', nameGap: 24, nameTextStyle: AXIS_LABEL,
      axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtNum(v) }, splitLine: { show: false } },
    yAxis: { type: 'value', axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtUSD(v, 0) }, splitLine: SPLIT_LINE, scale: true },
    series: [{
      type: 'line', data: est.sweep.map(p => [p.committed, round2(p.monthly)]), showSymbol: false, smooth: false,
      lineStyle: { color: SERIES_COLORS[0], width: 2 }, itemStyle: { color: SERIES_COLORS[0] },
      markPoint: best ? {
        symbol: 'circle', symbolSize: 10, itemStyle: { color: COMMITTED_COLOR },
        label: { color: '#e4e4e7', fontSize: 10, position: 'right', offset: [6, 0], formatter: `best ${fmtNum(best.committed)}` },
        data: [{ coord: [best.committed, round2(best.monthly)] }],
      } : undefined,
      markLine: committedNow > 0 ? {
        symbol: 'none', silent: true, lineStyle: { color: '#e4e4e7', type: 'dashed' },
        label: { color: '#a1a1aa', fontSize: 10, formatter: 'yours', position: 'end' },
        data: [{ xAxis: committedNow }],
      } : undefined,
    }],
  };

  return (
    <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
      {/* Inputs */}
      <div className="space-y-4">
        <Card title="Usage pattern" subtitle="A typical 24-hour day, repeated all month">
          <div className="mb-4">
            <NumField label="Constant baseline" value={baseline} onChange={setBaseline} unit="slots" step={step} disabled={!canBaseline} />
            {!canBaseline && <p className="text-[11px] text-zinc-600 mt-1">{ed.label} edition is autoscaling only; no baseline slots.</p>}
          </div>
          <div className="flex items-center justify-between mb-2">
            <GroupLabel>Peak windows (added to baseline)</GroupLabel>
            <button onClick={addWindow} className="flex items-center gap-1 text-[10px] font-medium text-cyan-400/80 hover:text-cyan-300 cursor-pointer mb-2">
              <Plus size={12} /> Add
            </button>
          </div>
          <div className="space-y-2">
            {windows.map((w, i) => (
              <div key={w.id} className="flex items-end gap-2 p-2 rounded-lg border border-zinc-800/40" style={{ background: '#0c0c0f' }}>
                <span className="h-2 w-2 rounded-full mb-3 shrink-0" style={{ background: SERIES_COLORS[(i + 1) % SERIES_COLORS.length] }} />
                <div className="grid grid-cols-3 gap-2 flex-1 min-w-0">
                  <TimeField label="From" value={w.start} onChange={v => updateWindow(w.id, { start: v })} />
                  <TimeField label="To" value={w.end} onChange={v => updateWindow(w.id, { end: v })} />
                  <NumField label="Slots" value={w.slots} onChange={v => updateWindow(w.id, { slots: v })} step={step} />
                </div>
                <button onClick={() => removeWindow(w.id)} className="p-2 mb-0.5 text-zinc-600 hover:text-rose-400 cursor-pointer" title="Remove window">
                  <Trash2 size={13} />
                </button>
              </div>
            ))}
            {windows.length === 0 && <p className="text-[11px] text-zinc-600 px-1">No peaks. Baseline runs flat all day.</p>}
          </div>
        </Card>

        <Card title="Pricing" subtitle="us-central1 list, $/slot-hour"
          action={<ResetButton onClick={() => { resetRates(edition, term); setDiscount(0); }} />}>
          <div className="grid grid-cols-2 gap-3 mb-4">
            <SelectField label="Edition" value={edition} options={editions.map(e => ({ value: e.edition, label: e.label }))} onChange={changeEdition} />
            <NumField label="Pay-as-you-go" value={paygRate} onChange={setPaygRate} step={0.001} unit="$/slot-hr" />
          </div>
          <GroupLabel>Capacity commitment</GroupLabel>
          <div className="grid grid-cols-2 gap-3 mb-4">
            <NumField label="Committed slots" value={committed} onChange={setCommitted} step={step} unit="slots" disabled={!canCommit} />
            <SelectField label="Term" value={term} options={TERM_OPTIONS} onChange={changeTerm} disabled={!canCommit} />
            <NumField label="Commit rate" value={commitRate} onChange={setCommitRate} step={0.001} unit="$/slot-hr" disabled={!canCommit} />
            <NumField label="Discount" value={discount} onChange={setDiscount} unit="% off" step={1} />
          </div>
          {!canCommit && <p className="text-[11px] text-zinc-600">{ed.label} edition has no commitments; everything bills pay-as-you-go.</p>}
        </Card>
      </div>

      {/* Results */}
      <div className="lg:col-span-2 space-y-4">
        <CalcStatus error={error} warnings={est?.warnings} loading={loading} />
        {est && (
          <>
            <div className="grid grid-cols-2 xl:grid-cols-4 gap-4">
              <MetricCard label="Daily cost" value={fmtUSD(est.daily.total)} icon={<Clock size={18} />}
                detail={`${fmtUSD(est.daily.committed)} committed + ${fmtUSD(est.daily.payg)} PAYG`} accentColor={SERIES_COLORS[0]} />
              <MetricCard label="Monthly cost" value={fmtUSD(est.monthly.total, 0)} icon={<CalendarDays size={18} />}
                detail={`${presets.hours_per_month} h/month · ${paygShare.toFixed(0)}% pay-as-you-go`} accentColor={BASELINE_PAYG_COLOR} />
              <MetricCard label="Peak / average" value={`${fmtNum(est.peak_slots)} / ${fmtNum(est.avg_slots)}`} icon={<Activity size={18} />}
                detail={`slots · ${fmtNum(est.payg_slot_hours)} PAYG slot-hours/day`} accentColor={SERIES_COLORS[2]} />
              <MetricCard label="Commit utilization" value={committedNow > 0 ? `${(est.commit_utilization * 100).toFixed(0)}%` : '—'} icon={<Gauge size={18} />}
                detail={committedNow > 0
                  ? `${fmtNum(est.used_committed_slot_hours)} of ${fmtNum(est.committed_slot_hours)} slot-hours/day used`
                  : 'no commitment'} accentColor={COMMITTED_COLOR} />
            </div>

            <Card title="24-hour usage profile" subtitle="Baseline and peak windows stacked; dashed line is the commitment">
              <div className="h-[260px]"><ReactECharts option={usageOption} style={{ height: '100%' }} notMerge /></div>
            </Card>

            <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
              <Card title="Hourly cost" subtitle="Commitment bills flat; baseline above it and autoscaled bursts bill pay-as-you-go">
                <div className="h-[220px]"><ReactECharts option={costOption} style={{ height: '100%' }} notMerge /></div>
              </Card>
              <Card title="Monthly cost vs commitment" subtitle={canCommit ? `Sweep in ${step}-slot steps at ${ed.label} ${term} rates` : `Not available for ${ed.label}`}>
                {canCommit ? (
                  <div className="h-[220px]"><ReactECharts option={sweepOption} style={{ height: '100%' }} notMerge /></div>
                ) : (
                  <div className="h-[220px] flex items-center justify-center text-xs text-zinc-600">Switch to Enterprise or Enterprise Plus to compare commitments.</div>
                )}
              </Card>
            </div>

            <div className="rounded-xl border border-zinc-800/40 px-4 py-3 text-xs text-zinc-500 leading-relaxed" style={{ background: '#111114' }}>
              Peak windows add slots on top of the baseline; a window ending at or before its start wraps past midnight.
              Baselines, commitments and autoscaling all move in {step}-slot steps, so demand is rounded up before billing.
              Committed slots bill 24×7 for the whole term whether used or not; baseline above the commitment bills 24×7 at pay-as-you-go.
              Standard edition autoscales only and is capped at 1,600 slots per reservation. Idle-slot sharing, the one-minute
              autoscale minimum and on-demand queries are not modelled.
            </div>
          </>
        )}
      </div>
    </div>
  );
}

function TimeField({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  return (
    <div className="min-w-0">
      <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">{label}</label>
      <input
        type="time"
        value={value}
        step={900}
        onChange={e => { if (e.target.value) onChange(e.target.value); }}
        className="w-full min-w-0 text-xs text-zinc-200 font-mono rounded-lg px-1.5 py-2 outline-none border border-zinc-800/50 focus:border-cyan-500/30 [color-scheme:dark]"
        style={{ background: '#09090b' }}
      />
    </div>
  );
}

function area(name: string, data: number[], color: string) {
  return {
    name, type: 'line', stack: 'usage', data: [...data, data[data.length - 1]], showSymbol: false, step: 'end',
    lineStyle: { color, width: 2 }, itemStyle: { color },
    areaStyle: { color, opacity: 0.25 },
    emphasis: { focus: 'series' },
  };
}

function costBar(name: string, data: number[], color: string) {
  return {
    name, type: 'bar', stack: 'cost', data: data.map(round2), barWidth: '60%',
    itemStyle: { color, borderColor: '#111114', borderWidth: 1 },
  };
}

function round2(n: number): number {
  return Math.round(n * 100) / 100;
}

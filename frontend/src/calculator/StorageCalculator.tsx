import { useMemo, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { Database, HardDrive, Box, CalendarDays } from 'lucide-react';
import { MetricCard } from '../dashboards/shared';
import { postStorageCalculation } from '../api';
import type { CalculatorPresets, StorageCalcRequest, StorageRates, StorageUnit, StorageVolumes } from '../types';
import { useCalculation } from './useCalculation';
import { Card, GroupLabel, NumField, SelectField, ResetButton, CalcStatus } from './shared';
import { fmtUSD, fmtNum, CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE } from './format';

const UNIT_GIB: Record<StorageUnit, number> = { GiB: 1, TiB: 1024, PiB: 1024 ** 2 };
const UNIT_OPTIONS = (Object.keys(UNIT_GIB) as StorageUnit[]).map(u => ({ value: u, label: u }));

const DEFAULT_VOLUMES: StorageVolumes = {
  active_logical: 100, long_term_logical: 50, active_physical: 30, long_term_physical: 15, fail_safe: 3,
};

const LOGICAL_COLOR = '#38bdf8';
const PHYSICAL_COLOR = '#c084fc';

export default function StorageCalculator({ presets }: { presets: CalculatorPresets }) {
  const regions = presets.storage_regions;
  const [region, setRegion] = useState(regions[0].region);
  const [unit, setUnit] = useState<StorageUnit>('TiB');
  const [vol, setVol] = useState<StorageVolumes>(DEFAULT_VOLUMES);
  const [rates, setRates] = useState<StorageRates>(regions[0].rates);
  const [discount, setDiscount] = useState(0);
  const [freeTier, setFreeTier] = useState(true);

  const regionPreset = regions.find(r => r.region === region) ?? regions[0];

  const req = useMemo<StorageCalcRequest>(
    () => ({ unit, volumes: vol, rates, discount_pct: discount, free_tier: freeTier }),
    [unit, vol, rates, discount, freeTier],
  );
  const { est, loading, error } = useCalculation(req, postStorageCalculation);

  const changeRegion = (next: string) => {
    setRegion(next);
    setRates((regions.find(r => r.region === next) ?? regions[0]).rates);
  };
  const changeUnit = (next: StorageUnit) => {
    const factor = UNIT_GIB[unit] / UNIT_GIB[next];
    setVol(Object.fromEntries(
      Object.entries(vol).map(([k, v]) => [k, Number((v * factor).toPrecision(6))]),
    ) as unknown as StorageVolumes);
    setUnit(next);
  };
  const setV = (k: keyof StorageVolumes) => (v: number) => setVol(prev => ({ ...prev, [k]: v }));
  const setR = (k: keyof StorageRates) => (v: number) => setRates(prev => ({ ...prev, [k]: v }));

  const logicalBytes = vol.active_logical + vol.long_term_logical;
  const physicalBytes = vol.active_physical + vol.long_term_physical + vol.fail_safe;

  const volumeOption = {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, ...CHART_TOOLTIP,
      valueFormatter: (v: number) => `${fmtNum(v, 2)} ${unit}` },
    legend: { bottom: 0, textStyle: { color: '#71717a', fontSize: 11 }, itemWidth: 10, itemHeight: 10 },
    grid: { left: 8, right: 16, top: 12, bottom: 36, containLabel: true },
    xAxis: { type: 'value', axisLabel: { ...AXIS_LABEL, formatter: (v: number) => `${fmtNum(v)} ${unit}` }, splitLine: SPLIT_LINE },
    yAxis: { type: 'category', data: ['Logical', 'Physical'], axisLabel: { color: '#a1a1aa', fontSize: 11 }, axisLine: { show: false }, axisTick: { show: false } },
    series: [
      bar('Active', [vol.active_logical, vol.active_physical], LOGICAL_COLOR),
      bar('Long-term', [vol.long_term_logical, vol.long_term_physical], PHYSICAL_COLOR),
      bar('Fail-safe', [0, vol.fail_safe], '#fb7185'),
    ],
  };

  const costOption = est && {
    ...volumeOption,
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, ...CHART_TOOLTIP,
      valueFormatter: (v: number) => fmtUSD(v) },
    xAxis: { type: 'value', axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtUSD(v, 0) }, splitLine: SPLIT_LINE },
    series: [
      bar('Active', [est.logical.active, est.physical.active], LOGICAL_COLOR),
      bar('Long-term', [est.logical.long_term, est.physical.long_term], PHYSICAL_COLOR),
      bar('Fail-safe', [0, est.physical.fail_safe], '#fb7185'),
    ],
  };

  return (
    <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
      {/* Inputs */}
      <div className="space-y-4">
        <Card title="Data volume" subtitle="Same data measured both ways"
          action={<div className="w-20"><SelectField label="Unit" value={unit} options={UNIT_OPTIONS} onChange={v => changeUnit(v as StorageUnit)} /></div>}>
          <GroupLabel>Logical (uncompressed)</GroupLabel>
          <div className="grid grid-cols-2 gap-3 mb-4">
            <NumField label="Active" value={vol.active_logical} onChange={setV('active_logical')} unit={unit} />
            <NumField label="Long-term" value={vol.long_term_logical} onChange={setV('long_term_logical')} unit={unit} />
          </div>
          <GroupLabel>Physical (compressed)</GroupLabel>
          <div className="grid grid-cols-2 gap-3">
            <NumField label="Active + time travel" value={vol.active_physical} onChange={setV('active_physical')} unit={unit} />
            <NumField label="Long-term" value={vol.long_term_physical} onChange={setV('long_term_physical')} unit={unit} />
            <NumField label="Fail-safe" value={vol.fail_safe} onChange={setV('fail_safe')} unit={unit} />
          </div>
        </Card>

        <Card title="Pricing" subtitle={`${regionPreset.label} list, $/GiB/month`}
          action={<ResetButton onClick={() => { setRates(regionPreset.rates); setDiscount(0); }} />}>
          <div className="grid grid-cols-2 gap-3 mb-4">
            <SelectField label="Region" value={region} options={regions.map(r => ({ value: r.region, label: r.label }))} onChange={changeRegion} />
            <NumField label="Discount" value={discount} onChange={setDiscount} unit="% off" step={1} />
          </div>
          <div className="grid grid-cols-2 gap-3 mb-4">
            <NumField label="Active logical" value={rates.active_logical} onChange={setR('active_logical')} step={0.001} unit="$" />
            <NumField label="Long-term logical" value={rates.long_term_logical} onChange={setR('long_term_logical')} step={0.001} unit="$" />
            <NumField label="Active physical" value={rates.active_physical} onChange={setR('active_physical')} step={0.001} unit="$" />
            <NumField label="Long-term physical" value={rates.long_term_physical} onChange={setR('long_term_physical')} step={0.001} unit="$" />
          </div>
          <label className="flex items-center gap-2 text-xs text-zinc-400 cursor-pointer">
            <input type="checkbox" checked={freeTier} onChange={e => setFreeTier(e.target.checked)} className="accent-cyan-400" />
            {presets.free_tier_gib} GiB free tier per storage SKU
          </label>
        </Card>
      </div>

      {/* Results */}
      <div className="lg:col-span-2 space-y-4">
        <CalcStatus error={error} loading={loading} />
        {est && (
          <>
            <div className="grid grid-cols-2 xl:grid-cols-4 gap-4">
              <MetricCard label="Logical billing" value={fmtUSD(est.logical.total)} icon={<Database size={18} />}
                detail={`per month · ${fmtNum(logicalBytes, 2)} ${unit}`} accentColor={LOGICAL_COLOR} />
              <MetricCard label="Physical billing" value={fmtUSD(est.physical.total)} icon={<HardDrive size={18} />}
                detail={`per month · ${fmtNum(physicalBytes, 2)} ${unit} incl. fail-safe`} accentColor={PHYSICAL_COLOR} />
              <MetricCard label="Recommended" value={est.cheaper === 'logical' ? 'Logical' : 'Physical'} icon={<Box size={18} />}
                detail={`saves ${fmtUSD(est.savings)}/mo · ${est.effective_compression_ratio.toFixed(2)}x vs ${est.break_even_ratio.toFixed(2)}x break-even`} accentColor="#4ade80" />
              <MetricCard label="Annual" value={fmtUSD(est.annual_recommended, 0)} icon={<CalendarDays size={18} />}
                detail={`12 × ${fmtUSD(est.annual_recommended / 12)} on the recommended model`} accentColor="#fbbf24" />
            </div>

            <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
              <Card title="Data footprint" subtitle="Logical vs physical volume by tier">
                <div className="h-[220px]"><ReactECharts option={volumeOption} style={{ height: '100%' }} notMerge /></div>
              </Card>
              <Card title="Monthly cost" subtitle="Each model billed on its own bytes and rates">
                <div className="h-[220px]"><ReactECharts option={costOption} style={{ height: '100%' }} notMerge /></div>
              </Card>
            </div>

            <div className="rounded-xl border border-zinc-800/40 px-4 py-3 text-xs text-zinc-500 leading-relaxed" style={{ background: '#111114' }}>
              Your data compresses <span className="text-zinc-300 font-mono">{est.data_compression_ratio.toFixed(2)}x</span>
              {' '}({est.effective_compression_ratio.toFixed(2)}x once time travel and fail-safe are counted). At these rates physical billing
              wins above <span className="text-zinc-300 font-mono">{est.break_even_ratio.toFixed(2)}x</span>, the ratio of the blended physical
              to logical rate for your active/long-term mix. Billing model is set per dataset; estimates exclude query, streaming and egress charges.
            </div>
          </>
        )}
      </div>
    </div>
  );
}

function bar(name: string, data: number[], color: string) {
  return {
    name, type: 'bar', stack: 'total', data, barWidth: 22,
    itemStyle: { color, borderColor: '#111114', borderWidth: 1 },
    emphasis: { focus: 'series' },
  };
}

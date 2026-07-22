import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { Activity, Coins, Fuel } from 'lucide-react';
import type { CryptoPulseData, CryptoChainPulse } from '../../types';
import { fetchCryptoPulse } from '../../api';
import { MetricCard, EmptyState, ErrorBanner } from '../../dashboards/shared';
import {
  BTC_COLOR, ETH_COLOR, CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE,
  fmtNum, Panel, DaysPicker,
} from './shared';

const DAY_OPTIONS = [7, 30, 90, 365];
const ADDRESS_MAX_DAYS = 90; // mirrors backend cryptoAddressMaxDays

// Aligns both chains' series on the union of dates so lines share one x-axis.
function mergeDates(btc: { date: string }[], eth: { date: string }[]): string[] {
  return [...new Set([...btc.map(r => r.date), ...eth.map(r => r.date)])].sort();
}

function seriesByDate<T extends { date: string }>(
  rows: T[], dates: string[], pick: (r: T) => number,
): (number | null)[] {
  const byDate = new Map(rows.map(r => [r.date, pick(r)]));
  return dates.map(d => byDate.get(d) ?? null);
}

function dualChainLineOption(
  dates: string[],
  btcData: (number | null)[], ethData: (number | null)[],
  btcName: string, ethName: string, dualY: boolean,
) {
  return {
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
    legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
    grid: { left: 60, right: dualY ? 60 : 20, top: 32, bottom: 24 },
    xAxis: { type: 'category', data: dates, axisLabel: AXIS_LABEL },
    yAxis: dualY
      ? [
          { type: 'value', axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtNum(v) }, splitLine: SPLIT_LINE },
          { type: 'value', axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtNum(v) }, splitLine: { show: false } },
        ]
      : { type: 'value', axisLabel: { ...AXIS_LABEL, formatter: (v: number) => fmtNum(v) }, splitLine: SPLIT_LINE },
    series: [
      { name: btcName, type: 'line', data: btcData, showSymbol: false, lineStyle: { color: BTC_COLOR, width: 2 }, itemStyle: { color: BTC_COLOR } },
      { name: ethName, type: 'line', data: ethData, showSymbol: false, yAxisIndex: dualY ? 1 : 0, lineStyle: { color: ETH_COLOR, width: 2 }, itemStyle: { color: ETH_COLOR } },
    ],
  };
}

function KpiRow({ chain, label, color }: { chain: CryptoChainPulse; label: string; color: string }) {
  const k = chain.kpi;
  return (
    <>
      <MetricCard label={`${label} Transactions`} value={fmtNum(k.tx_count)} icon={<Activity size={15} />} detail={k.date || 'no data'} accentColor={color} />
      <MetricCard label={`${label} Value Settled`} value={fmtNum(k.value_settled)} icon={<Coins size={15} />} detail={`native units · ${k.blocks} blocks`} accentColor={color} />
      <MetricCard label={`${label} Fees`} value={fmtNum(k.fees_total)} icon={<Fuel size={15} />} detail={`block fullness ${k.fullness_pct}%`} accentColor={color} />
    </>
  );
}

export default function PulseTab() {
  const [days, setDays] = useState(90);
  const [data, setData] = useState<CryptoPulseData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchCryptoPulse(days)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [days]);

  if (error) return <div className="space-y-4"><DaysPicker options={DAY_OPTIONS} value={days} onChange={setDays} /><ErrorBanner message={error} /></div>;
  if (loading || !data) return <EmptyState text="Loading network pulse…" />;

  const dates = mergeDates(data.btc.daily, data.eth.daily);
  const blockDates = mergeDates(data.btc.blocks, data.eth.blocks);
  const addrDates = mergeDates(data.btc.addresses, data.eth.addresses);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <DaysPicker options={DAY_OPTIONS} value={days} onChange={setDays} />
        <span className="text-[11px] text-zinc-600">Complete UTC days · latest = yesterday</span>
      </div>

      <div className="grid grid-cols-2 lg:grid-cols-3 gap-3">
        <KpiRow chain={data.btc} label="BTC" color={BTC_COLOR} />
        <KpiRow chain={data.eth} label="ETH" color={ETH_COLOR} />
      </div>

      <div className="grid lg:grid-cols-2 gap-4">
        <Panel title="Daily Transactions">
          <ReactECharts
            style={{ height: 260 }}
            option={dualChainLineOption(dates,
              seriesByDate(data.btc.daily, dates, r => r.tx_count),
              seriesByDate(data.eth.daily, dates, r => r.tx_count),
              'BTC txs', 'ETH txs', true)}
          />
        </Panel>
        <Panel
          title="Value Settled (native units)"
          note="BTC totals include change outputs returning to the sender — an upper bound on economic volume"
        >
          <ReactECharts
            style={{ height: 260 }}
            option={dualChainLineOption(dates,
              seriesByDate(data.btc.daily, dates, r => r.value_settled),
              seriesByDate(data.eth.daily, dates, r => r.value_settled),
              'BTC settled', 'ETH settled', true)}
          />
        </Panel>
        <Panel
          title="Active Addresses (approx. distinct senders)"
          note={days > ADDRESS_MAX_DAYS ? 'capped at 90 days — pick a shorter range' : 'APPROX_COUNT_DISTINCT, ~1% error'}
        >
          {days > ADDRESS_MAX_DAYS ? (
            <EmptyState text="Address counts are limited to 90-day windows to bound scan cost." />
          ) : (
            <ReactECharts
              style={{ height: 260 }}
              option={dualChainLineOption(addrDates,
                seriesByDate(data.btc.addresses, addrDates, r => r.active_addresses),
                seriesByDate(data.eth.addresses, addrDates, r => r.active_addresses),
                'BTC senders', 'ETH senders', true)}
            />
          )}
        </Panel>
        <Panel title="Congestion — Block Fullness %" note="BTC: avg weight / 4M limit · ETH: avg gas_used / gas_limit">
          <ReactECharts
            style={{ height: 260 }}
            option={dualChainLineOption(blockDates,
              seriesByDate(data.btc.blocks, blockDates, r => r.fullness_pct),
              seriesByDate(data.eth.blocks, blockDates, r => r.fullness_pct),
              'BTC fullness', 'ETH fullness', false)}
          />
        </Panel>
      </div>

      <Panel title="Block Production (blocks per day)" note="BTC targets ~144/day with variance · ETH holds ~7,100/day on its 12s slot cadence">
        <ReactECharts
          style={{ height: 220 }}
          option={{
            tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
            legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
            grid: { left: 60, right: 60, top: 32, bottom: 24 },
            xAxis: { type: 'category', data: blockDates, axisLabel: AXIS_LABEL },
            yAxis: [
              { type: 'value', axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE },
              { type: 'value', axisLabel: AXIS_LABEL, splitLine: { show: false } },
            ],
            series: [
              { name: 'BTC blocks', type: 'bar', data: seriesByDate(data.btc.blocks, blockDates, r => r.blocks), itemStyle: { color: BTC_COLOR, opacity: 0.8 } },
              { name: 'ETH blocks', type: 'line', yAxisIndex: 1, showSymbol: false, data: seriesByDate(data.eth.blocks, blockDates, r => r.blocks), lineStyle: { color: ETH_COLOR, width: 2 }, itemStyle: { color: ETH_COLOR } },
            ],
          }}
        />
      </Panel>
    </div>
  );
}

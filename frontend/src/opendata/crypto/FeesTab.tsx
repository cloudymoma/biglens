import { useState, useEffect } from 'react';
import ReactECharts from 'echarts-for-react';
import { Gauge, Flame, Pickaxe } from 'lucide-react';
import type { CryptoFeesData } from '../../types';
import { fetchCryptoFees } from '../../api';
import { MetricCard, EmptyState, ErrorBanner } from '../../dashboards/shared';
import {
  BTC_COLOR, ETH_COLOR, CHART_TOOLTIP, AXIS_LABEL, SPLIT_LINE,
  fmtNum, Panel, DaysPicker, mergeDates, seriesByDate,
} from './shared';

const DAY_OPTIONS = [7, 30, 90, 365];

// Pairs each day's block fullness with its fee level for the scatter.
function congestionPoints(
  blocks: { date: string; fullness_pct: number }[],
  feeByDate: Map<string, number>,
): [number, number][] {
  return blocks
    .filter(b => feeByDate.has(b.date))
    .map(b => [b.fullness_pct, feeByDate.get(b.date) as number]);
}

export default function FeesTab() {
  const [days, setDays] = useState(90);
  const [data, setData] = useState<CryptoFeesData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchCryptoFees(days)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [days]);

  if (error) return <div className="space-y-4"><DaysPicker options={DAY_OPTIONS} value={days} onChange={setDays} /><ErrorBanner message={error} /></div>;
  if (loading || !data) return <EmptyState text="Loading fee market…" />;

  const latestBtc = data.btc[data.btc.length - 1];
  const latestEth = data.eth[data.eth.length - 1];
  const btcDates = data.btc.map(r => r.date);
  const ethDates = data.eth.map(r => r.date);
  const feeTrendDates = mergeDates(data.btc, data.eth);
  const btcFeeByDate = new Map(data.btc.map(r => [r.date, r.median_fee_vb]));
  const ethFeeByDate = new Map(data.eth.map(r => [r.date, r.avg_gas_gwei]));

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <DaysPicker options={DAY_OPTIONS} value={days} onChange={setDays} />
        <span className="text-[11px] text-zinc-600">Complete UTC days · latest = yesterday</span>
      </div>

      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <MetricCard label="BTC Median Fee" value={latestBtc ? `${latestBtc.median_fee_vb} sat/vB` : '—'} icon={<Gauge size={15} />} detail={latestBtc?.date || 'no data'} accentColor={BTC_COLOR} />
        <MetricCard label="BTC Total Fees" value={latestBtc ? `${fmtNum(latestBtc.total_fees_btc)} BTC` : '—'} icon={<Pickaxe size={15} />} detail={`subsidy ${fmtNum(latestBtc?.subsidy_btc ?? 0)} BTC`} accentColor={BTC_COLOR} />
        <MetricCard label="ETH Avg Gas Price" value={latestEth ? `${latestEth.avg_gas_gwei} gwei` : '—'} icon={<Gauge size={15} />} detail={latestEth?.date || 'no data'} accentColor={ETH_COLOR} />
        <MetricCard label="ETH Burned" value={latestEth ? `${fmtNum(latestEth.burned_eth)} ETH` : '—'} icon={<Flame size={15} />} detail={`tips ${fmtNum(latestEth?.tips_eth ?? 0)} ETH`} accentColor={ETH_COLOR} />
      </div>

      <Panel title="Fee Trend" note="BTC median sat/vB (left) vs ETH gas-weighted avg gwei (right)">
        <ReactECharts
          style={{ height: 260 }}
          option={{
            tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
            legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
            grid: { left: 60, right: 60, top: 32, bottom: 24 },
            xAxis: { type: 'category', data: feeTrendDates, axisLabel: AXIS_LABEL },
            yAxis: [
              { type: 'value', axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE },
              { type: 'value', axisLabel: AXIS_LABEL, splitLine: { show: false } },
            ],
            series: [
              { name: 'BTC sat/vB', type: 'line', showSymbol: false, data: seriesByDate(data.btc, feeTrendDates, r => r.median_fee_vb), lineStyle: { color: BTC_COLOR, width: 2 }, itemStyle: { color: BTC_COLOR } },
              { name: 'ETH gwei', type: 'line', showSymbol: false, yAxisIndex: 1, data: seriesByDate(data.eth, feeTrendDates, r => r.avg_gas_gwei), lineStyle: { color: ETH_COLOR, width: 2 }, itemStyle: { color: ETH_COLOR } },
            ],
          }}
        />
      </Panel>

      <div className="grid lg:grid-cols-2 gap-4">
        <Panel title="BTC Miner Revenue" note="daily coinbase split: block subsidy vs transaction fees (BTC)">
          <ReactECharts
            style={{ height: 260 }}
            option={{
              tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
              legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
              grid: { left: 60, right: 20, top: 32, bottom: 24 },
              xAxis: { type: 'category', data: btcDates, axisLabel: AXIS_LABEL },
              yAxis: { type: 'value', axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE },
              series: [
                { name: 'Subsidy', type: 'bar', stack: 'rev', data: data.btc.map(r => r.subsidy_btc), itemStyle: { color: '#71717a' } },
                { name: 'Fees', type: 'bar', stack: 'rev', data: data.btc.map(r => r.total_fees_btc), itemStyle: { color: BTC_COLOR } },
              ],
            }}
          />
        </Panel>
        <Panel title="ETH Burned vs Tips" note="EIP-1559: base fee burned vs validator priority tips (ETH)">
          <ReactECharts
            style={{ height: 260 }}
            option={{
              tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
              legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
              grid: { left: 60, right: 20, top: 32, bottom: 24 },
              xAxis: { type: 'category', data: ethDates, axisLabel: AXIS_LABEL },
              yAxis: { type: 'value', axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE },
              series: [
                { name: 'Burned', type: 'bar', stack: 'fees', data: data.eth.map(r => r.burned_eth), itemStyle: { color: '#ef4444', opacity: 0.85 } },
                { name: 'Tips', type: 'bar', stack: 'fees', data: data.eth.map(r => r.tips_eth), itemStyle: { color: ETH_COLOR } },
              ],
            }}
          />
        </Panel>
      </div>

      <Panel title="Cost of Congestion" note="each dot = one day · x: block fullness % · y: fee level (native fee units)">
        <ReactECharts
          style={{ height: 260 }}
          option={{
            tooltip: { ...CHART_TOOLTIP, formatter: (p: { seriesName: string; value: [number, number] }) => `${p.seriesName}<br/>fullness ${p.value[0]}% · fee ${p.value[1]}` },
            legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
            grid: { left: 60, right: 20, top: 32, bottom: 36 },
            xAxis: { type: 'value', name: 'fullness %', nameTextStyle: AXIS_LABEL, axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE },
            yAxis: { type: 'log', axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE },
            series: [
              { name: 'BTC (sat/vB)', type: 'scatter', symbolSize: 7, data: congestionPoints(data.btc_blocks, btcFeeByDate), itemStyle: { color: BTC_COLOR, opacity: 0.7 } },
              { name: 'ETH (gwei)', type: 'scatter', symbolSize: 7, data: congestionPoints(data.eth_blocks, ethFeeByDate), itemStyle: { color: ETH_COLOR, opacity: 0.7 } },
            ],
          }}
        />
      </Panel>
    </div>
  );
}

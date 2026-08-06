import { useEffect, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { DollarSign, PiggyBank, Boxes, TrendingUp } from 'lucide-react';
import { billingParams, fetchBillingOverview } from '../api';
import type { BillingFilterState, BillingMeta, BillingOverviewData } from '../types';
import { EmptyState, ErrorBanner, MetricCard } from '../dashboards/shared';
import { AXIS_LABEL, CHART_TOOLTIP, GROSS_COLOR, NET_COLOR, Panel, fmtMoney } from './shared';

interface TabProps {
  filter: BillingFilterState;
  meta: BillingMeta;
}

export default function OverviewTab({ filter, meta }: TabProps) {
  const [data, setData] = useState<BillingOverviewData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const paramsKey = JSON.stringify(billingParams(filter));

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchBillingOverview(filter)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paramsKey]);

  if (error) return <ErrorBanner message={error} />;
  if (loading || !data) return <EmptyState text="Loading cost overview…" />;
  if (data.kpis.length === 0) return <EmptyState text="No billing rows in this window." />;

  const cur = meta.dataset.currency || data.kpis[0].currency;
  const kpi = data.kpis[0];
  const multiCurrency = data.kpis.length > 1;

  const lineOption = {
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
    grid: { left: 60, right: 16, top: 24, bottom: 24 },
    legend: { textStyle: { color: '#a1a1aa', fontSize: 10 } },
    xAxis: { type: 'category', data: data.daily.map(d => d.date), axisLabel: AXIS_LABEL },
    yAxis: { type: 'value', axisLabel: AXIS_LABEL },
    series: [
      { name: 'Gross', type: 'line', showSymbol: false, data: data.daily.map(d => d.gross), lineStyle: { color: GROSS_COLOR }, itemStyle: { color: GROSS_COLOR } },
      { name: 'Net', type: 'line', showSymbol: false, data: data.daily.map(d => d.net), lineStyle: { color: NET_COLOR }, itemStyle: { color: NET_COLOR } },
    ],
  };

  const donutOption = {
    tooltip: { trigger: 'item', ...CHART_TOOLTIP },
    series: [{
      type: 'pie',
      radius: ['45%', '75%'],
      label: { color: '#a1a1aa', fontSize: 10 },
      data: data.top_services.map(s => ({ name: s.name, value: Math.max(s.net, 0) })),
    }],
  };

  const barOption = {
    tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
    grid: { left: 120, right: 16, top: 8, bottom: 24 },
    xAxis: { type: 'value', axisLabel: AXIS_LABEL },
    yAxis: { type: 'category', data: data.top_projects.map(p => p.name).reverse(), axisLabel: AXIS_LABEL },
    series: [{ type: 'bar', data: data.top_projects.map(p => p.net).reverse(), itemStyle: { color: NET_COLOR } }],
  };

  return (
    <div className="space-y-4">
      {multiCurrency && (
        <ErrorBanner message={`This dataset contains ${data.kpis.length} currencies; KPI cards show ${kpi.currency} only. Filter by billing account for exact figures.`} />
      )}
      <div className="grid grid-cols-2 md:grid-cols-5 gap-3">
        <MetricCard label="Net cost" value={fmtMoney(kpi.net, cur)} icon={<DollarSign size={16} />} detail="gross + credits" accentColor="#34d399" />
        <MetricCard label="Gross cost" value={fmtMoney(kpi.gross, cur)} icon={<DollarSign size={16} />} detail="before credits" accentColor="#60a5fa" />
        <MetricCard label="Credits" value={fmtMoney(kpi.credits, cur)} icon={<PiggyBank size={16} />} detail="CUD, SUD, promos" accentColor="#f59e0b" />
        <MetricCard label="Projects · Services" value={`${kpi.projects} · ${kpi.services}`} icon={<Boxes size={16} />} detail="active in window" accentColor="#a78bfa" />
        <MetricCard
          label="Projected month (net)"
          value={data.projected_month_net === null ? '—' : fmtMoney(data.projected_month_net, cur)}
          icon={<TrendingUp size={16} />}
          detail={data.projected_month_net === null ? 'window not in current month' : 'MTD + 7-day run-rate'}
          accentColor="#f472b6"
        />
      </div>
      <Panel title="Daily cost" note={filter.invoiceMonth ? `invoice ${filter.invoiceMonth}` : `${filter.start} → ${filter.end}`}>
        <ReactECharts style={{ height: 280 }} option={lineOption} />
      </Panel>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <Panel title="Top services (net)">
          <ReactECharts style={{ height: 260 }} option={donutOption} />
        </Panel>
        <Panel title="Top projects (net)">
          <ReactECharts style={{ height: 260 }} option={barOption} />
        </Panel>
      </div>
    </div>
  );
}

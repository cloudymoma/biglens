import { useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { fmtPct } from '../common/format';
import { ASSET_COLOR, axisStyle, baseOption, chrome, colorOf, tipHtml } from './charts';
import type { StableAsset } from './deeplink';
import EventsFeed from './EventsFeed';
import { fmtBps, fmtDateTime, fmtMoney, fmtNative, fmtPrice } from './format';
import SignalChip from './SignalChip';
import SignalMatrix from './SignalMatrix';
import { cx } from './styles';
import { useStableTheme } from './theme';
import type { StableOverview, StableOverviewCard, StableSupplyDay, StableWatchlist } from './types';
import { ColdState, CsvButton, ErrorNote, KV, Panel, SegmentedControl } from './ui';

// Overview (stablecoin.md §7.2, tab 1): asset cards, the risk matrix, the
// events feed, supply history and the watchlist market table.

const REGIME_LABEL: Record<string, string> = { 'EU-MiCA': 'EU MiCA', 'US-GENIUS': 'US GENIUS' };

function NeutralBadge({ children, title }: { children: string; title?: string }) {
  return (
    <span title={title} className={`inline-flex rounded-md border px-1.5 py-0.5 text-[10px] ${cx.border} ${cx.text}`}>
      {children}
    </span>
  );
}

function flow(card: StableOverviewCard, window: string): string {
  const n = card.net_issuance.find(x => x.window === window);
  if (!n || n.net_native === null) return '—';
  const sign = n.net_native > 0 ? '+' : '';
  return `${sign}${fmtNative(n.net_native, card.symbol)} (${fmtPct(n.net_pct, 2, true)})${n.source ? ` · ${n.source}` : ''}`;
}

function AssetCard({ card, warmingUp }: { card: StableOverviewCard; warmingUp: boolean }) {
  return (
    <div className={`${cx.card} space-y-2`}>
      <div className="flex items-center justify-between gap-2">
        <h3 className={`text-base font-bold ${cx.strong}`}>{card.symbol}</h3>
        <SignalChip state={card.worst_signal} prefix="worst" />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <span className={`font-mono text-lg font-semibold ${cx.strong}`}>{fmtPrice(card.consensus, card.numeraire)}</span>
        <SignalChip state={card.peg_signal} value={fmtBps(card.dev_bps)} title="Deviation from par; the chip state is S1 (peg)" />
      </div>
      {warmingUp && <p className={`text-[11px] ${cx.muted}`}>Warming up: waiting for the first supply sample.</p>}
      <div>
        <KV k={`Circulating${card.circulating_source ? ` (${card.circulating_source})` : ''}`}>
          <span className="font-mono">{fmtNative(card.circulating_native, card.symbol)}</span>
        </KV>
        <KV k="24h flow">
          <span className="font-mono">{flow(card, '24h')}</span>
        </KV>
        <KV k="7d flow">
          <span className="font-mono">{flow(card, '7d')}</span>
        </KV>
        {card.asset === 'usdt' ? (
          <KV k="Reserve coverage">
            <span className="font-mono" title={card.coverage_as_of}>
              {card.coverage === null ? '—' : card.coverage.toFixed(4)}
            </span>
          </KV>
        ) : (
          <KV k="Attestation age">
            {card.attestation ? (
              <span title={`Latest attestation recorded by BigLens: period ending ${card.attestation.period_end}, published ${card.attestation.published} (${card.attestation.firm})`}>
                <span className="font-mono">{card.attestation.age_days} d</span> since {card.attestation.period_end}
              </span>
            ) : (
              '—'
            )}
          </KV>
        )}
        <KV k="Freezes (30 d)">
          <span className="font-mono">{card.freezes_30d === null ? '—' : card.freezes_30d.toLocaleString('en-US')}</span>
        </KV>
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        {Object.entries(card.regulatory ?? {}).map(([regime, status]) => (
          <NeutralBadge key={regime} title="Curated regulatory status (Reserves & Regulation tab has sources)">
            {`${REGIME_LABEL[regime] ?? regime}: ${status.replace(/_/g, ' ')}`}
          </NeutralBadge>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <SignalChip state={card.control_state} prefix="S8" />
        <span className={`text-[11px] ${cx.muted}`}>{card.control_summary || '—'}</span>
      </div>
    </div>
  );
}

type Units = 'usd' | 'native';

function supplyOption(theme: 'light' | 'dark', series: { asset: string; days: StableSupplyDay[] }[], units: Units) {
  const c = chrome(theme);
  const ax = axisStyle(theme);
  const days = [...new Set(series.flatMap(s => s.days.map(d => d.day)))].sort();
  const byDay = (s: { days: StableSupplyDay[] }) => new Map(s.days.map(d => [d.day, d]));
  const mixed = units === 'native' && series.some(s => s.asset === 'eurc') && series.some(s => s.asset !== 'eurc');
  const top = series.map(s => {
    const m = byDay(s);
    const native = s.asset === 'eurc' && mixed;
    return {
      name: s.asset.toUpperCase(),
      type: 'line',
      stack: units === 'usd' ? 'supply' : undefined,
      areaStyle: units === 'usd' ? { opacity: 0.35 } : undefined,
      showSymbol: false,
      xAxisIndex: 0,
      yAxisIndex: native ? 1 : 0,
      color: ASSET_COLOR[s.asset],
      data: days.map(d => {
        const r = m.get(d);
        if (!r) return null;
        return units === 'usd' ? r.aggregator_circulating_usd : (r.issuer_circulating_native ?? r.aggregator_circulating_native);
      }),
    };
  });
  const bars = series.map(s => {
    const m = byDay(s);
    return {
      name: `${s.asset.toUpperCase()} net issuance`,
      type: 'bar',
      xAxisIndex: 1,
      yAxisIndex: s.asset === 'eurc' && series.length > 1 ? 3 : 2,
      color: ASSET_COLOR[s.asset],
      data: days.map(d => m.get(d)?.aggregator_change_native ?? null),
    };
  });
  const unitFor = (name: string): string => {
    const a = name.split(' ')[0];
    if (units === 'usd' && !name.includes('net issuance')) return 'USD';
    return a;
  };
  const base = baseOption(
    theme,
    `Daily supply of ${series.map(s => s.asset.toUpperCase()).join(', ')} (${units === 'usd' ? 'USD-equivalent, stacked' : 'native units'}) with daily net issuance bars, from DefiLlama.`,
  );
  return {
    ...base,
    tooltip: {
      ...base.tooltip,
      trigger: 'axis',
      formatter: (params: { axisValueLabel?: string; seriesName?: string; value?: unknown; color?: unknown }[]) => {
        const rows = params
          .filter(p => typeof p.value === 'number')
          .map(p => {
            const name = p.seriesName ?? '';
            const v = p.value as number;
            const unit = unitFor(name);
            return {
              label: name,
              value: unit === 'USD' ? fmtMoney(v, 'USD') : fmtNative(v, unit),
              color: colorOf(p.color),
            };
          });
        return tipHtml(params[0]?.axisValueLabel ?? '', rows);
      },
    },
    legend: { ...base.legend, type: 'scroll' },
    grid: [
      { left: 64, right: mixed || series.length > 1 ? 64 : 24, top: 30, height: '52%' },
      { left: 64, right: series.length > 1 ? 64 : 24, top: '72%', height: '18%' },
    ],
    xAxis: [
      { type: 'category', data: days, gridIndex: 0, ...ax, axisLabel: { show: false } },
      { type: 'category', data: days, gridIndex: 1, ...ax },
    ],
    yAxis: [
      {
        type: 'value',
        gridIndex: 0,
        scale: units === 'native',
        ...ax,
        axisLabel: { ...ax.axisLabel, formatter: (v: number) => (units === 'usd' ? fmtMoney(v, 'USD', 0) : fmtNative(v, '').trim()) },
      },
      {
        type: 'value',
        gridIndex: 0,
        scale: true,
        show: mixed,
        position: 'right',
        ...ax,
        splitLine: { show: false },
        axisLabel: { ...ax.axisLabel, formatter: (v: number) => `€${fmtNative(v, '').trim()}` },
      },
      { type: 'value', gridIndex: 1, ...ax, axisLabel: { ...ax.axisLabel, formatter: (v: number) => fmtNative(v, '').trim() } },
      {
        type: 'value',
        gridIndex: 1,
        show: series.length > 1,
        position: 'right',
        ...ax,
        splitLine: { show: false },
        axisLabel: { ...ax.axisLabel, formatter: (v: number) => `€${fmtNative(v, '').trim()}` },
      },
    ],
    series: [...top, ...bars],
    textStyle: { color: c.text },
  };
}

function SupplyHistory({ data, asset }: { data: StableOverview; asset: StableAsset }) {
  const theme = useStableTheme();
  const [units, setUnits] = useState<Units>('usd');
  const series = data.supply_history.filter(s => (asset === 'all' || s.asset === asset) && s.days.length > 0);
  return (
    <Panel
      title="Supply history"
      help="Daily circulating supply (DefiLlama, normalized to native units, §5.1; USD-equivalent uses DefiLlama's USD value) with daily net issuance bars (change in DefiLlama circulating). In native mode EURC is in € on the right axis."
      right={
        <>
          <SegmentedControl
            label="Units"
            options={[
              { id: 'usd', label: 'USD-equivalent' },
              { id: 'native', label: 'Native' },
            ]}
            value={units}
            onChange={setUnits}
          />
          <CsvButton
            filename={`stablecoin-supply-history-${asset}-${data.range}.csv`}
            label="supply history"
            headers={['day', 'asset', 'issuer_circulating_native', 'aggregator_circulating_native', 'aggregator_circulating_usd', 'aggregator_change_native']}
            rows={() =>
              series.flatMap(s =>
                s.days.map(d => [d.day, s.asset, d.issuer_circulating_native, d.aggregator_circulating_native, d.aggregator_circulating_usd, d.aggregator_change_native]),
              )
            }
          />
        </>
      }
    >
      {series.length === 0 ? (
        <ColdState>Loading history: the DefiLlama bootstrap runs at boot and then daily.</ColdState>
      ) : (
        <ReactECharts option={supplyOption(theme, series, units)} notMerge style={{ height: 360 }} />
      )}
    </Panel>
  );
}

function MarketTable({ w }: { w: StableWatchlist }) {
  return (
    <Panel
      title="Market (watchlist)"
      help="The 15 largest USD-pegged stablecoins and every EUR-pegged one on DefiLlama (§2.1). Supply is native (value ÷ price, §5.1); deviation is in each coin's own currency (EUR coins through the EUR/USD consensus). DefiLlama's classification is shown as-is."
      right={
        <CsvButton
          filename="stablecoin-watchlist.csv"
          label="watchlist"
          headers={['symbol', 'name', 'peg_type', 'peg_mechanism', 'circulating_native', 'circulating_usd', 'change_1d_pct', 'change_7d_pct', 'change_30d_pct', 'price_usd', 'dev_bps', 'share_pct', 'note']}
          rows={() =>
            w.rows.map(r => [r.symbol, r.name, r.peg_type, r.peg_mechanism, r.circulating_native, r.circulating_usd, r.change_1d_pct, r.change_7d_pct, r.change_30d_pct, r.price_usd, r.dev_bps, r.share_pct, r.note ?? ''])
          }
        />
      }
    >
      {w.rows.length === 0 ? (
        <ColdState>Waiting for the first DefiLlama sample.</ColdState>
      ) : (
        <>
          <div className="mb-2 flex flex-wrap gap-4 text-[11px]">
            <span className={cx.muted}>
              USD coins <span className={`font-mono ${cx.text}`}>{fmtMoney(w.usd_total_usd, 'USD')}</span>
            </span>
            <span className={cx.muted}>
              EUR coins <span className={`font-mono ${cx.text}`}>{fmtMoney(w.eur_total_usd, 'USD')}</span>
            </span>
            <span className={cx.muted}>
              EUR share of USD+EUR <span className={`font-mono ${cx.text}`}>{fmtPct(w.eur_share_pct, 3)}</span>
            </span>
            <span className={cx.muted}>
              EUR/USD <span className={`font-mono ${cx.text}`}>{w.fx_eur_usd === null ? '—' : w.fx_eur_usd.toFixed(4)}</span>
            </span>
            <span className={cx.faint}>
              DefiLlama · as of {fmtDateTime(w.as_of)}
              {w.stale && ' · stale'}
            </span>
          </div>
          <div className="overflow-x-auto">
            <table className={cx.table}>
              <thead>
                <tr>
                  {['Coin', 'Peg', 'Mechanism', 'Supply', 'Supply (USD)', '1d', '7d', '30d', 'Price (USD)', 'Deviation', 'Share'].map(h => (
                    <th key={h} scope="col" className={cx.th}>
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {w.rows.map(r => (
                  <tr key={r.llama_id} className={`${cx.tr} ${r.core ? 'bg-emerald-600/5 dark:bg-emerald-500/5' : ''}`}>
                    <td className={cx.td}>
                      <span className={`font-semibold ${cx.strong}`}>{r.symbol}</span> <span className={cx.muted}>{r.name}</span>
                    </td>
                    <td className={cx.td}>{r.numeraire}</td>
                    <td className={cx.td}>{r.peg_mechanism || '—'}</td>
                    <td className={`${cx.td} font-mono`}>{fmtNative(r.circulating_native, r.symbol)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtMoney(r.circulating_usd, 'USD')}</td>
                    <td className={`${cx.td} font-mono`}>{fmtPct(r.change_1d_pct, 2, true)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtPct(r.change_7d_pct, 2, true)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtPct(r.change_30d_pct, 2, true)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtPrice(r.price_usd, 'USD', 4)}</td>
                    <td className={`${cx.td} font-mono`}>
                      {fmtBps(r.dev_bps)}
                      {r.note && <span className={`ml-1.5 font-sans text-[11px] ${cx.muted}`}>{r.note}</span>}
                    </td>
                    <td className={`${cx.td} font-mono`}>{fmtPct(r.share_pct, 2)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className={`mt-2 text-[11px] ${cx.faint}`}>{w.note}</p>
        </>
      )}
    </Panel>
  );
}

export default function OverviewTab({
  asset,
  data,
  loading,
  error,
}: {
  asset: StableAsset;
  data: StableOverview | null;
  loading: boolean;
  error: string;
}) {
  if (!data) {
    return error ? <ErrorNote message={error} /> : <ColdState>{loading ? 'Loading the overview…' : 'No data'}</ColdState>;
  }
  const cards = data.cards.filter(c => asset === 'all' || c.asset === asset);
  const matrix = data.matrix.filter(r => asset === 'all' || r.asset === asset);
  const riskMatrix = (
    <Panel
      title="Risk matrix"
      help="Each signal is a rule with its own evaluation policy (sampled with hysteresis, confirmed, or level-triggered; §6.1). There is no composite score: the asset summary is the worst state (info counts as normal). Unknown means insufficient data, never normal."
    >
      <SignalMatrix rows={matrix} />
    </Panel>
  );
  return (
    <div className="space-y-5">
      {error && <ErrorNote message={error} />}
      {cards.length === 1 ? (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
          <AssetCard card={cards[0]} warmingUp={data.warming_up} />
          <div className="lg:col-span-2">{riskMatrix}</div>
        </div>
      ) : (
        <>
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
            {cards.map(c => (
              <AssetCard key={c.asset} card={c} warmingUp={data.warming_up} />
            ))}
          </div>
          {riskMatrix}
        </>
      )}
      <SupplyHistory data={data} asset={asset} />
      <EventsFeed events={data.events} asset={asset} />
      <MarketTable w={data.watchlist} />
    </div>
  );
}

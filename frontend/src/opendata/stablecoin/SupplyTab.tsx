import { useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { fmtPct } from '../common/format';
import { usePolling } from '../common/usePolling';
import { fetchStableSupply } from './api';
import { ASSET_COLOR, axisStyle, baseOption, chrome, colorOf, tipHtml } from './charts';
import type { StableAsset, StableRange } from './deeplink';
import { fmtDateTime, fmtMoney, fmtNative } from './format';
import SignalMatrix from './SignalMatrix';
import type { Theme } from './signals';
import { cx } from './styles';
import { useStableTheme } from './theme';
import type { StableMatrixRow, StableSupplyAsset, StableSupplyRow, StableWatchlist } from './types';
import { ColdState, CsvButton, ErrorNote, Panel, SegmentedControl, Sources, Stat } from './ui';

// Supply & Flows (stablecoin.md §7.2, tab 3): the supply definitions (§5.1)
// and their reconciliation (S12), net issuance, daily supply, the per-chain
// distribution with 7-day migration, and EURC's share of the EUR market.

type Source = 'issuer' | 'aggregator';

const onchainSum = (rows: StableSupplyRow[]): { total: number | null; chains: number } => {
  const v = rows.filter(r => r.verified && r.onchain_total_native !== null);
  return { total: v.length ? v.reduce((s, r) => s + (r.onchain_total_native ?? 0), 0) : null, chains: v.length };
};

// delta7d is a chain's 7-day change in native units from its current supply
// and its 7-day percentage change.
const delta7d = (r: StableSupplyRow): number | null => {
  const cur = r.issuer_circulating_native ?? r.aggregator_circulating_native;
  if (cur === null || r.change_7d_pct === null || r.change_7d_pct <= -100) return null;
  return cur - cur / (1 + r.change_7d_pct / 100);
};

function Definitions({ a, matrix }: { a: StableSupplyAsset; matrix: StableMatrixRow[] }) {
  const on = onchainSum(a.chains);
  const agg = a.reconcile.aggregator;
  const s12 = matrix.find(r => r.asset === a.asset)?.signals.find(s => s.id === 'S12')?.state;
  return (
    <Panel
      title="Supply definitions"
      help="Issuer circulating: the issuer's own figure (Circle API; Tether: Σ per-chain circulating = total liabilities). On-chain total: totalSupply / SPL mint supply read by BigLens on verified deployments. Inventory: on-chain − issuer per chain (minted but not circulating). Aggregator: DefiLlama, normalized to native units (§5.1). S12 compares issuer and DefiLlama (> 0.5% watch, > 2% warning) and issuer vs on-chain per chain."
    >
      <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
        <Stat label="Issuer circulating" value={fmtNative(a.totals.issuer_circulating_native, a.symbol)} sub={`as of ${fmtDateTime(a.totals.issuer_as_of)}`} />
        <Stat label={`On-chain total (${on.chains} verified chain${on.chains === 1 ? '' : 's'})`} value={fmtNative(on.total, a.symbol)} sub={a.reconcile.onchain_as_of ? `read ${fmtDateTime(a.reconcile.onchain_as_of)}` : 'not read yet'} />
        <Stat label={`Inventory (${a.totals.inventory_chains} chain${a.totals.inventory_chains === 1 ? '' : 's'})`} value={fmtNative(a.totals.inventory_native, a.symbol)} sub="on-chain − issuer" />
        <Stat
          label="DefiLlama circulating"
          value={fmtNative(a.totals.aggregator_circulating_native, a.symbol)}
          sub={`${fmtMoney(a.totals.aggregator_circulating_usd, 'USD')} · ${fmtDateTime(a.totals.aggregator_as_of)}`}
        />
      </div>
      <div className={`mt-3 flex flex-wrap items-center gap-3 text-xs ${cx.text}`}>
        <span>
          DefiLlama vs issuer: <span className="font-mono">{fmtPct(agg.diff_pct, 3, true)}</span>
          <span className={cx.muted}>
            {' '}
            (watch &gt; {agg.watch_pct}%, warning &gt; {agg.warning_pct}%){agg.skip_reason ? ` · skipped: ${agg.skip_reason}` : ''}
          </span>
        </span>
        {s12 && <span className={cx.muted}>S12 state: {s12}</span>}
      </div>
    </Panel>
  );
}

function issuanceOption(theme: Theme, a: StableSupplyAsset, source: Source) {
  const ax = axisStyle(theme);
  const days = a.history.map(d => d.day);
  const base = baseOption(theme, `${a.symbol} daily supply (${source === 'issuer' ? 'issuer' : 'DefiLlama'}) with daily net issuance bars in ${a.symbol}.`);
  return {
    ...base,
    tooltip: {
      ...base.tooltip,
      trigger: 'axis',
      formatter: (params: { axisValueLabel?: string; seriesName?: string; value?: unknown; color?: unknown }[]) =>
        tipHtml(
          params[0]?.axisValueLabel ?? '',
          params.map(p => ({ label: p.seriesName ?? '', value: fmtNative(typeof p.value === 'number' ? p.value : null, a.symbol), color: colorOf(p.color) })),
        ),
    },
    grid: [
      { left: 72, right: 24, top: 30, height: '55%' },
      { left: 72, right: 24, top: '74%', height: '16%' },
    ],
    xAxis: [
      { type: 'category', data: days, gridIndex: 0, ...ax, axisLabel: { show: false } },
      { type: 'category', data: days, gridIndex: 1, ...ax },
    ],
    yAxis: [
      { type: 'value', gridIndex: 0, scale: true, ...ax, axisLabel: { ...ax.axisLabel, formatter: (v: number) => fmtNative(v, '').trim() } },
      { type: 'value', gridIndex: 1, ...ax, axisLabel: { ...ax.axisLabel, formatter: (v: number) => fmtNative(v, '').trim() } },
    ],
    series: [
      {
        name: source === 'issuer' ? 'Issuer circulating' : 'DefiLlama circulating',
        type: 'line',
        showSymbol: false,
        color: ASSET_COLOR[a.asset],
        xAxisIndex: 0,
        yAxisIndex: 0,
        data: a.history.map(d => (source === 'issuer' ? d.issuer_circulating_native : d.aggregator_circulating_native)),
      },
      {
        name: 'Net issuance (DefiLlama, daily)',
        type: 'bar',
        xAxisIndex: 1,
        yAxisIndex: 1,
        color: ASSET_COLOR[a.asset],
        data: a.history.map(d => d.aggregator_change_native),
      },
    ],
  };
}

function treemapOption(theme: Theme, a: StableSupplyAsset) {
  const c = chrome(theme);
  const data = a.chains
    .map(r => ({ name: r.chain_label, value: r.issuer_circulating_native ?? r.aggregator_circulating_native ?? 0, share: r.issuer_share_pct, tier: r.tier ?? '' }))
    .filter(d => d.value > 0);
  const base = baseOption(theme, `${a.symbol} circulating supply by chain as a treemap (${data.length} chains).`);
  return {
    ...base,
    tooltip: {
      ...base.tooltip,
      formatter: (p: { data?: { name: string; value: number; share: number | null; tier: string } }) =>
        p.data
          ? tipHtml(p.data.name, [
              { label: 'circulating', value: fmtNative(p.data.value, a.symbol) },
              { label: 'share', value: fmtPct(p.data.share, 2) },
              { label: 'representation', value: p.data.tier || '—' },
            ])
          : '',
    },
    series: [
      {
        type: 'treemap',
        roam: false,
        nodeClick: false,
        breadcrumb: { show: false },
        width: '100%',
        height: '100%',
        label: { color: '#ffffff', fontSize: 11, formatter: '{b}' },
        itemStyle: { borderColor: theme === 'dark' ? '#111114' : '#ffffff', borderWidth: 2, gapWidth: 2 },
        color: [ASSET_COLOR[a.asset]],
        colorMappingBy: 'value',
        levels: [{ colorSaturation: [0.35, 0.75] }],
        data,
      },
    ],
    textStyle: { color: c.text },
  };
}

function migrationOption(theme: Theme, a: StableSupplyAsset) {
  const ax = axisStyle(theme);
  const c = chrome(theme);
  // The 12 largest moves either way, gainers on top.
  const rows = a.chains
    .map(r => ({ label: r.chain_label, d: delta7d(r) }))
    .filter((x): x is { label: string; d: number } => x.d !== null && Math.abs(x.d) > 0)
    .sort((x, y) => Math.abs(y.d) - Math.abs(x.d))
    .slice(0, 12)
    .sort((x, y) => x.d - y.d);
  const base = baseOption(theme, `${a.symbol} 7-day supply change by chain in ${a.symbol} (chains that gained or lost supply).`);
  return {
    ...base,
    tooltip: {
      ...base.tooltip,
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      formatter: (params: { name?: string; value?: number }[]) =>
        tipHtml(params[0]?.name ?? '', [{ label: '7d change', value: `${(params[0]?.value ?? 0) > 0 ? '+' : ''}${fmtNative(params[0]?.value ?? null, a.symbol)}` }]),
    },
    grid: { left: 8, right: 24, top: 8, bottom: 24, containLabel: true },
    xAxis: { type: 'value', ...ax, axisLabel: { ...ax.axisLabel, formatter: (v: number) => fmtNative(v, '').trim() } },
    yAxis: { type: 'category', data: rows.map(r => r.label), ...ax, splitLine: { show: false } },
    series: [
      {
        type: 'bar',
        data: rows.map(r => ({ value: r.d, itemStyle: { color: r.d >= 0 ? ASSET_COLOR[a.asset] : c.muted } })),
      },
    ],
  };
}

// Issuer daily rows needed before the chart defaults to the issuer source.
const MIN_ISSUER_DAYS = 7;

function AssetSupply({ a, matrix, watchlist }: { a: StableSupplyAsset; matrix: StableMatrixRow[]; watchlist: StableWatchlist | null }) {
  const theme = useStableTheme();
  const [source, setSource] = useState<Source | null>(null); // null: automatic
  const issuerDays = a.history.filter(d => d.issuer_circulating_native !== null).length;
  const effSource: Source = source ?? (issuerDays >= MIN_ISSUER_DAYS ? 'issuer' : 'aggregator');
  const rowsWithMigration = a.chains.some(r => delta7d(r) !== null);
  const eur = a.asset === 'eurc' && watchlist ? watchlist.rows.filter(r => r.numeraire === 'EUR') : [];
  const eurTotal = eur.reduce((s, r) => s + (r.circulating_native ?? 0), 0);
  const eurc = eur.find(r => r.core === 'eurc');
  return (
    <section className="space-y-4" aria-label={`${a.symbol} supply`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className={`text-base font-bold ${cx.strong}`}>{a.symbol} supply</h2>
        <Sources sources={a.sources} />
      </div>
      <Definitions a={a} matrix={matrix} />
      <div className="grid grid-cols-1 gap-2 md:grid-cols-3">
        {a.net_issuance.map(n => (
          <Stat
            key={n.window}
            label={`Net issuance ${n.window}${n.source ? ` (${n.source === 'aggregator' ? 'aggregator' : 'issuer snapshots'})` : ''}`}
            value={n.net_native === null ? '—' : `${n.net_native > 0 ? '+' : ''}${fmtNative(n.net_native, a.symbol)}`}
            sub={`${fmtPct(n.net_pct, 2, true)}${n.from ? ` · ${fmtDateTime(n.from)} → ${fmtDateTime(n.to)}` : ''}`}
          />
        ))}
      </div>
      <Panel
        title="Daily supply and net issuance"
        help="Daily supply from the issuer's snapshots (BigLens rollup) or DefiLlama; bars are DefiLlama's day-over-day change. Until issuer snapshots span a window, flows come from DefiLlama (labeled aggregator)."
        right={
          <>
            <SegmentedControl
              label="Supply source"
              options={[
                { id: 'issuer', label: 'Issuer' },
                { id: 'aggregator', label: 'DefiLlama' },
              ]}
              value={effSource}
              onChange={setSource}
            />
            <CsvButton
              filename={`stablecoin-supply-${a.asset}.csv`}
              label={`${a.symbol} daily supply`}
              headers={['day', 'issuer_circulating_native', 'aggregator_circulating_native', 'aggregator_circulating_usd', 'aggregator_change_native']}
              rows={() => a.history.map(d => [d.day, d.issuer_circulating_native, d.aggregator_circulating_native, d.aggregator_circulating_usd, d.aggregator_change_native])}
            />
          </>
        }
      >
        {a.history.length === 0 ? (
          <ColdState>Loading history: the DefiLlama bootstrap runs at boot.</ColdState>
        ) : (
          <>
            {effSource === 'aggregator' && issuerDays < MIN_ISSUER_DAYS && (
              <p className={`mb-2 text-[11px] ${cx.muted}`}>
                Issuer snapshots cover {issuerDays} day{issuerDays === 1 ? '' : 's'} so far; DefiLlama is shown until they span a week.
              </p>
            )}
            <ReactECharts option={issuanceOption(theme, a, effSource)} notMerge style={{ height: 300 }} />
          </>
        )}
      </Panel>
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Panel
          title="Per-chain distribution"
          help="Issuer circulating by chain (DefiLlama where the issuer gives no per-chain figure)."
          right={
            <CsvButton
              filename={`stablecoin-distribution-${a.asset}.csv`}
              label={`${a.symbol} distribution by chain`}
              headers={['chain', 'circulating_native', 'source', 'share_pct']}
              rows={() =>
                a.chains.map(r => [
                  r.chain_label,
                  r.issuer_circulating_native ?? r.aggregator_circulating_native,
                  r.issuer_circulating_native !== null ? 'issuer' : 'defillama',
                  r.issuer_share_pct,
                ])
              }
            />
          }
        >
          {a.chains.length === 0 ? <ColdState>Waiting for the first supply sample.</ColdState> : <ReactECharts option={treemapOption(theme, a)} notMerge style={{ height: 300 }} />}
        </Panel>
        <Panel
          title="Chain migration (7 days)"
          help="Each chain's 7-day change in native units: current supply × (1 − 1 ÷ (1 + 7d change)); the 12 largest moves. Chains that gained supply are colored; chains that lost it are grey."
          right={
            <CsvButton
              filename={`stablecoin-migration-${a.asset}.csv`}
              label={`${a.symbol} 7-day chain migration`}
              headers={['chain', 'change_7d_native', 'change_7d_pct']}
              rows={() => a.chains.filter(r => delta7d(r) !== null).map(r => [r.chain_label, delta7d(r), r.change_7d_pct])}
            />
          }
        >
          {rowsWithMigration ? <ReactECharts option={migrationOption(theme, a)} notMerge style={{ height: 300 }} /> : <ColdState>7-day changes appear once DefiLlama or issuer history covers a week.</ColdState>}
        </Panel>
      </div>
      <Panel
        title="Chains"
        help="Issuer circulating, on-chain total (verified deployments only), inventory and DefiLlama per chain; 1d/7d/30d change from the issuer history, else DefiLlama (labeled)."
        right={
          <CsvButton
            filename={`stablecoin-chains-${a.asset}.csv`}
            label={`${a.symbol} chains`}
            headers={['chain', 'tier', 'token_label', 'verified', 'issuer_circulating_native', 'issuer_share_pct', 'onchain_total_native', 'inventory_native', 'aggregator_circulating_native', 'change_1d_pct', 'change_7d_pct', 'change_30d_pct', 'change_source']}
            rows={() =>
              a.chains.map(r => [
                r.chain_label,
                r.tier ?? '',
                r.token_label ?? '',
                r.verified ? 'yes' : 'no',
                r.issuer_circulating_native,
                r.issuer_share_pct,
                r.onchain_total_native,
                r.inventory_native,
                r.aggregator_circulating_native,
                r.change_1d_pct,
                r.change_7d_pct,
                r.change_30d_pct,
                r.change_source ?? '',
              ])
            }
          />
        }
      >
        {a.chains.length === 0 ? (
          <ColdState>Waiting for the first supply sample.</ColdState>
        ) : (
          <div className="max-h-[420px] overflow-auto">
            <table className={cx.table}>
              <thead>
                <tr>
                  {['Chain', 'Representation', 'Issuer circulating', 'Share', 'On-chain total', 'Inventory', 'DefiLlama', '1d', '7d', '30d'].map(h => (
                    <th key={h} scope="col" className={cx.th}>
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {a.chains.map(r => (
                  <tr key={r.chain} className={cx.tr}>
                    <td className={cx.td}>
                      <span className={`font-medium ${cx.strong}`}>{r.chain_label}</span>
                      {r.unmapped && <span className={`ml-1 text-[10px] ${cx.muted}`}>(unmapped)</span>}
                    </td>
                    <td className={cx.td}>
                      {r.tier || '—'}
                      {r.token_label ? ` · ${r.token_label}` : ''}
                    </td>
                    <td className={`${cx.td} font-mono`}>{fmtNative(r.issuer_circulating_native, a.symbol)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtPct(r.issuer_share_pct, 2)}</td>
                    <td className={`${cx.td} font-mono`} title={r.onchain_as_of}>
                      {r.verified ? fmtNative(r.onchain_total_native, a.symbol) : 'not verified'}
                      {r.onchain_stale && ' · stale'}
                    </td>
                    <td className={`${cx.td} font-mono`}>{fmtNative(r.inventory_native, a.symbol)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtNative(r.aggregator_circulating_native, a.symbol)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtPct(r.change_1d_pct, 2, true)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtPct(r.change_7d_pct, 2, true)}</td>
                    <td className={`${cx.td} font-mono`} title={r.change_source ? `source: ${r.change_source}` : undefined}>
                      {fmtPct(r.change_30d_pct, 2, true)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {a.unmapped_chains.length > 0 && (
          <details className={`mt-2 text-[11px] ${cx.muted}`}>
            <summary className={`cursor-pointer ${cx.focus}`}>
              {a.unmapped_chains.length} source chain names are not in the canonical chain map (listed as their own rows, never dropped)
            </summary>
            <p className="mt-1">{a.unmapped_chains.map(u => `${u.name} (${u.source})`).join(', ')}</p>
          </details>
        )}
      </Panel>
      <Panel
        title="Reconciliation"
        help="S12, issuer vs on-chain: a warning needs the issuer's circulating to exceed the on-chain total by more than max(0.1%, 1M units) in 2 consecutive hourly samples taken ≤ 15 min apart (USDT: 2 consecutive Tether updates, snapshots ≤ 7 days old)."
        right={
          <CsvButton
            filename={`stablecoin-reconcile-${a.asset}.csv`}
            label={`${a.symbol} reconciliation`}
            headers={['chain', 'method', 'issuer_native', 'onchain_native', 'excess_native', 'tolerance_native', 'aligned', 'exceeds_tolerance', 'consecutive', 'confirmed', 'skip_reason']}
            rows={() =>
              a.reconcile.onchain.map(r => [
                r.chain,
                r.method,
                r.issuer_native,
                r.onchain_native,
                r.excess_native,
                r.tolerance_native,
                r.aligned ? 'yes' : 'no',
                r.exceeds_tolerance ? 'yes' : 'no',
                r.method === 'latest_update' ? r.consecutive_updates : r.consecutive_samples,
                r.confirmed ? 'yes' : 'no',
                r.skip_reason ?? '',
              ])
            }
          />
        }
      >
        {a.reconcile.onchain.length === 0 ? (
          <ColdState>No on-chain comparison yet (needs an RPC read and an issuer figure for the same chain).</ColdState>
        ) : (
          <div className="overflow-x-auto">
            <table className={cx.table}>
              <thead>
                <tr>
                  {['Chain', 'Method', 'Issuer', 'On-chain', 'Issuer − on-chain', 'Tolerance', 'Status'].map(h => (
                    <th key={h} scope="col" className={cx.th}>
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {a.reconcile.onchain.map(r => (
                  <tr key={r.chain} className={cx.tr}>
                    <td className={cx.td}>{r.chain}</td>
                    <td className={cx.td}>{r.method.replace('_', ' ')}</td>
                    <td className={`${cx.td} font-mono`}>{fmtNative(r.issuer_native, a.symbol)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtNative(r.onchain_native, a.symbol)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtNative(r.excess_native, a.symbol)}</td>
                    <td className={`${cx.td} font-mono`}>{fmtNative(r.tolerance_native, a.symbol)}</td>
                    <td className={cx.td}>
                      {r.skip_reason
                        ? `skipped: ${r.skip_reason}`
                        : r.confirmed
                          ? 'over tolerance (confirmed)'
                          : r.exceeds_tolerance
                            ? 'over tolerance (awaiting confirmation)'
                            : 'within tolerance'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {(a.reconcile.tether_authorized ?? []).length > 0 && (
          <div className="mt-3">
            <h4 className={`mb-1 text-xs font-semibold ${cx.text}`}>Tether totalAuthorized vs on-chain totalSupply (info)</h4>
            <ul className={`space-y-0.5 text-[11px] ${cx.muted}`}>
              {(a.reconcile.tether_authorized ?? []).map(t => (
                <li key={t.chain}>
                  {t.chain}: {t.result}
                  {t.diff_native !== null ? ` (diff ${fmtNative(t.diff_native, a.symbol)})` : ''}
                  {t.skip_reason ? ` · ${t.skip_reason}` : ''}
                </li>
              ))}
            </ul>
          </div>
        )}
        {a.data_health.reasons.length > 0 && (
          <ul className="mt-3 space-y-0.5 text-[11px] text-amber-800 dark:text-amber-300">
            {a.data_health.reasons.map(r => (
              <li key={`${r.code}-${r.source}`}>
                {r.code}: {r.detail}
              </li>
            ))}
          </ul>
        )}
      </Panel>
      {a.asset === 'eurc' && (
        <Panel
          title="EUR stablecoin market"
          help="EURC's share among all EUR-pegged coins on DefiLlama, on native € amounts after the §5.1 normalization (every EUR coin is in the watchlist)."
        >
          {eur.length === 0 ? (
            <ColdState>Waiting for the DefiLlama watchlist.</ColdState>
          ) : (
            <div className="grid grid-cols-1 gap-2 md:grid-cols-3">
              <Stat label="EURC share of EUR stablecoins" value={eurc?.circulating_native != null && eurTotal > 0 ? fmtPct((eurc.circulating_native / eurTotal) * 100, 1) : '—'} />
              <Stat label="EUR stablecoins (native)" value={`€${fmtNative(eurTotal, '').trim()}`} sub={`${eur.length} coins`} />
              <Stat label="Largest EUR coins" value={eur.slice(0, 3).map(r => r.symbol).join(', ')} />
            </div>
          )}
        </Panel>
      )}
    </section>
  );
}

export default function SupplyTab({
  asset,
  range,
  active,
  refreshTick,
  matrix,
  watchlist,
}: {
  asset: StableAsset;
  range: StableRange;
  active: boolean;
  refreshTick: number;
  matrix: StableMatrixRow[];
  watchlist: StableWatchlist | null;
}) {
  const poll = usePolling(() => fetchStableSupply(asset, range), 60_000, `stable-supply-${asset}-${range}-${refreshTick}`, active);
  const data = poll.data;
  if (!data) {
    return poll.error ? <ErrorNote message={poll.error} /> : <ColdState>Loading supply…</ColdState>;
  }
  const rows = matrix.filter(r => asset === 'all' || r.asset === asset);
  return (
    <div className="space-y-6">
      {poll.error && <ErrorNote message={poll.error} />}
      {data.warming_up && <ColdState>Waiting for the first supply sample (DefiLlama, Circle, Tether and RPC reads start at boot).</ColdState>}
      <Panel
        title="Supply signals"
        help="S4 flows (spike-filtered 24h / 7d change on a 3-reading median), S10 chain (bridged share), S12 reconciliation, S13 data health."
      >
        <SignalMatrix rows={rows} only={['S4', 'S10', 'S12', 'S13']} />
      </Panel>
      {data.assets.map(a => (
        <AssetSupply key={a.asset} a={a} matrix={matrix} watchlist={watchlist} />
      ))}
    </div>
  );
}

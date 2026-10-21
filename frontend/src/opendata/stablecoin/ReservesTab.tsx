import ReactECharts from 'echarts-for-react';
import { fmtPct } from '../common/format';
import { usePolling } from '../common/usePolling';
import { fetchStableReserves } from './api';
import { axisStyle, baseOption, colorOf, tipHtml } from './charts';
import type { StableAsset } from './deeplink';
import { fmtDateTime, fmtMoney } from './format';
import SignalChip from './SignalChip';
import type { Theme } from './signals';
import { cx } from './styles';
import { useStableTheme } from './theme';
import type { StableRegime, StableReserveMix, StableReservesAsset, StableTetherPoint } from './types';
import { ColdState, CsvButton, ErrorNote, ExtLink, KV, Panel, Stat } from './ui';

// Reserves & Regulation (stablecoin.md §7.2, tab 4): Tether's reported
// assets, liabilities and coverage (BigLens snapshots), the curated reserve
// mix and attestation timeline with ages, the issuer profile and redemption
// terms, and the regulatory matrix with key dates. Curated facts carry an
// as-of date and a source; unknown stays unknown (§4.5). The yield proxy is
// v1.1.

const BUCKET_LABEL: Record<string, string> = {
  cash_deposits: 'Cash & bank deposits',
  tbills_le93d: 'T-bills ≤ 93 days',
  repo: 'Reverse repo',
  gov_mmf: 'Government MMFs',
  other_tbills: 'Other T-bills',
  gold: 'Gold',
  btc: 'Bitcoin',
  secured_loans: 'Secured loans',
  other: 'Other',
};

// Bucket colors: ordered from cash-like to riskier; Okabe–Ito based.
const BUCKET_COLOR: Record<string, string> = {
  cash_deposits: '#0072B2',
  tbills_le93d: '#56B4E9',
  repo: '#009E73',
  gov_mmf: '#7FC8A9',
  other_tbills: '#9AB8D6',
  gold: '#E69F00',
  btc: '#D55E00',
  secured_loans: '#CC79A7',
  other: '#999999',
};

const REGIME_LABEL: Record<string, string> = { 'EU-MiCA': 'EU MiCA', 'US-GENIUS': 'US GENIUS' };

function StatusText({ status }: { status: string }) {
  return (
    <span className={`inline-flex rounded-md border px-1.5 py-0.5 text-[11px] font-medium ${cx.border} ${cx.strong}`}>{status.replace(/_/g, ' ')}</span>
  );
}

function tetherOption(theme: Theme, history: StableTetherPoint[]) {
  const ax = axisStyle(theme);
  const base = baseOption(
    theme,
    `Tether total assets and total liabilities in USD per Tether update with the coverage ratio (assets ÷ liabilities) on the right axis, and shareholder equity as bars below.`,
  );
  const x = history.map(h => h.as_of);
  const sym = history.length < 40;
  return {
    ...base,
    tooltip: {
      ...base.tooltip,
      trigger: 'axis',
      formatter: (params: { seriesName?: string; value?: unknown; color?: unknown; axisValue?: string }[]) =>
        tipHtml(
          fmtDateTime(params[0]?.axisValue),
          params.map(p => ({
            label: p.seriesName ?? '',
            value: typeof p.value !== 'number' ? '—' : p.seriesName === 'Coverage' ? p.value.toFixed(4) : fmtMoney(p.value, 'USD'),
            color: colorOf(p.color),
          })),
        ),
    },
    grid: [
      { left: 72, right: 64, top: 32, height: '52%' },
      { left: 72, right: 64, top: '72%', height: '18%' },
    ],
    xAxis: [
      { type: 'category', data: x, gridIndex: 0, ...ax, axisLabel: { show: false } },
      { type: 'category', data: x, gridIndex: 1, ...ax, axisLabel: { ...ax.axisLabel, formatter: (v: string) => v.slice(0, 10) } },
    ],
    yAxis: [
      { type: 'value', gridIndex: 0, scale: true, ...ax, axisLabel: { ...ax.axisLabel, formatter: (v: number) => fmtMoney(v, 'USD', 1) } },
      {
        type: 'value',
        gridIndex: 0,
        scale: true,
        position: 'right',
        ...ax,
        splitLine: { show: false },
        axisLabel: { ...ax.axisLabel, formatter: (v: number) => v.toFixed(4) },
      },
      { type: 'value', gridIndex: 1, ...ax, axisLabel: { ...ax.axisLabel, formatter: (v: number) => fmtMoney(v, 'USD', 1) } },
    ],
    series: [
      { name: 'Total assets', type: 'line', xAxisIndex: 0, yAxisIndex: 0, color: '#0072B2', showSymbol: sym, data: history.map(h => h.total_assets_usd) },
      { name: 'Total liabilities', type: 'line', xAxisIndex: 0, yAxisIndex: 0, color: '#D55E00', showSymbol: sym, data: history.map(h => h.total_liabilities_usd) },
      {
        name: 'Coverage',
        type: 'line',
        xAxisIndex: 0,
        yAxisIndex: 1,
        color: '#CC79A7',
        lineStyle: { type: 'dashed' },
        showSymbol: sym,
        data: history.map(h => h.coverage),
      },
      { name: 'Shareholder equity', type: 'bar', xAxisIndex: 1, yAxisIndex: 2, color: '#009E73', barMaxWidth: 28, data: history.map(h => h.shareholder_equity_usd) },
    ],
  };
}

function mixOption(theme: Theme, mix: StableReserveMix, symbol: string) {
  const ax = axisStyle(theme);
  const base = baseOption(theme, `${symbol} reserve mix as of ${mix.as_of} by bucket, as a stacked bar of shares in percent.`);
  return {
    ...base,
    tooltip: {
      ...base.tooltip,
      trigger: 'item',
      formatter: (p: { seriesName?: string; value?: unknown; color?: unknown }) =>
        tipHtml(p.seriesName ?? '', [{ label: 'share', value: fmtPct(typeof p.value === 'number' ? p.value : null, 2), color: colorOf(p.color) }], `as of ${mix.as_of}`),
    },
    legend: { ...base.legend, type: 'scroll', bottom: 0, top: undefined },
    grid: { left: 8, right: 8, top: 8, bottom: 40 },
    xAxis: { type: 'value', max: 100, ...ax, axisLabel: { ...ax.axisLabel, formatter: '{value}%' } },
    yAxis: { type: 'category', data: [mix.as_of], ...ax, axisLabel: { show: false } },
    series: mix.buckets.map(b => ({
      name: BUCKET_LABEL[b.id] ?? b.id,
      type: 'bar',
      stack: 'mix',
      color: BUCKET_COLOR[b.id] ?? '#999999',
      barWidth: 28,
      data: [b.share_pct],
    })),
  };
}

function AssetReserves({ a }: { a: StableReservesAsset }) {
  const theme = useStableTheme();
  const latestMix = a.reserve_mix[0];
  const latestAtt = a.attestations[0];
  const t = a.tether;
  return (
    <section className="space-y-4" aria-label={`${a.symbol} reserves`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className={`text-base font-bold ${cx.strong}`}>{a.symbol} reserves</h2>
        <div className="flex flex-wrap items-center gap-1.5">
          {a.signals.map(s => (
            <SignalChip key={s.id} state={s.state} prefix={s.id} title={`${s.dimension}: ${s.summary || s.rule}`} />
          ))}
          <span className={`text-[11px] ${cx.muted}`}>
            facts: {a.facts_status}
            {a.overridden.length > 0 && ` · operator override: ${a.overridden.join(', ')}`}
          </span>
        </div>
      </div>
      {a.facts_status === 'unknown' && (
        <ColdState>No curated facts for {a.symbol}: reserve, issuer and regulatory panels stay unknown rather than guessed.</ColdState>
      )}
      {t && (
        <Panel
          title="Tether: assets, liabilities and coverage"
          help="From Tether's transparency JSON, stored at every Tether update (new id). Coverage = total assets ÷ total liabilities (S5: < 1.010 watch, < 1.000 critical). The series starts at BigLens' first snapshot."
          right={
            <CsvButton
              filename="stablecoin-tether-reserves.csv"
              label="Tether reserves"
              headers={['update_id', 'as_of', 'total_assets_usd', 'total_liabilities_usd', 'shareholder_equity_usd', 'coverage']}
              rows={() => t.history.map(h => [h.update_id, h.as_of, h.total_assets_usd, h.total_liabilities_usd, h.shareholder_equity_usd, h.coverage])}
            />
          }
        >
          <div className="mb-3 grid grid-cols-2 gap-2 md:grid-cols-4">
            <Stat label="Coverage" value={t.latest?.coverage == null ? '—' : t.latest.coverage.toFixed(4)} sub={`update ${fmtDateTime(t.latest?.as_of)}`} />
            <Stat label="Total assets" value={fmtMoney(t.latest?.total_assets_usd ?? null, 'USD')} />
            <Stat label="Total liabilities" value={fmtMoney(t.latest?.total_liabilities_usd ?? null, 'USD')} />
            <Stat label="Shareholder equity" value={fmtMoney(t.latest?.shareholder_equity_usd ?? null, 'USD')} />
          </div>
          {t.history.length === 0 ? (
            <ColdState>The series starts at the first stored Tether snapshot.</ColdState>
          ) : (
            <ReactECharts option={tetherOption(theme, t.history)} notMerge style={{ height: 340 }} />
          )}
        </Panel>
      )}
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Panel
          title="Reserve mix"
          help="Curated from the issuer's latest report, mapped to fixed buckets (shares sum to 100 ± 0.5). Point-in-time; the bucket mapping and caveats are in the note."
          right={
            <CsvButton
              filename={`stablecoin-reserve-mix-${a.asset}.csv`}
              label={`${a.symbol} reserve mix`}
              headers={['as_of', 'bucket', 'share_pct', 'attested', 'source_url']}
              rows={() => a.reserve_mix.flatMap(m => m.buckets.map(b => [m.as_of, b.id, b.share_pct, m.attested === undefined ? '' : String(m.attested), m.source_url]))}
            />
          }
        >
          {!latestMix ? (
            <ColdState>Reserve mix: unknown (not curated).</ColdState>
          ) : (
            <>
              <ReactECharts option={mixOption(theme, latestMix, a.symbol)} notMerge style={{ height: 150 }} />
              <p className={`mt-2 text-[11px] ${cx.muted}`}>
                As of {latestMix.as_of}
                {latestMix.attested === false && ' · not attested'} · <ExtLink href={latestMix.source_url}>source</ExtLink>
                {latestMix.note ? ` · ${latestMix.note}` : ''}
              </p>
            </>
          )}
        </Panel>
        <Panel
          title="Attestations"
          help="Reserve reports recorded by BigLens (curated, newest first). Age counts from period_end of the latest report recorded by BigLens (S6); attestations_checked is when a maintainer last confirmed that no newer report exists."
          right={
            <CsvButton
              filename={`stablecoin-attestations-${a.asset}.csv`}
              label={`${a.symbol} attestations`}
              headers={['period_end', 'published', 'firm', 'type', 'reserves_native', 'liabilities_native', 'ratio', 'age_days', 'url']}
              rows={() => a.attestations.map(x => [x.period_end, x.published, x.firm, x.type, x.reserves_native ?? '', x.liabilities_native ?? '', x.ratio, x.age_days, x.url])}
            />
          }
        >
          {!latestAtt ? (
            <ColdState>Attestations: unknown (not curated).</ColdState>
          ) : (
            <>
              <div className="mb-2 grid grid-cols-2 gap-2">
                <Stat
                  label="Latest attestation recorded by BigLens"
                  value={`${latestAtt.age_days} days`}
                  sub={`period ending ${latestAtt.period_end} · ${latestAtt.firm}`}
                />
                <Stat label="Cadence · last checked" value={a.cadence_days ? `${a.cadence_days} days` : '—'} sub={a.attestations_checked ? `checked ${a.attestations_checked}` : 'not checked'} />
              </div>
              <div className="max-h-[220px] overflow-auto">
                <table className={cx.table}>
                  <thead>
                    <tr>
                      {['Period end', 'Published', 'Firm', 'Type', 'Ratio', 'Report'].map(h => (
                        <th key={h} scope="col" className={cx.th}>
                          {h}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {a.attestations.map(x => (
                      <tr key={`${x.period_end}-${x.firm}`} className={cx.tr}>
                        <td className={`${cx.td} font-mono`}>{x.period_end}</td>
                        <td className={`${cx.td} font-mono`}>{x.published || '—'}</td>
                        <td className={cx.td}>{x.firm}</td>
                        <td className={cx.td}>{x.type}</td>
                        <td className={`${cx.td} font-mono`}>{x.ratio === null ? '—' : x.ratio.toFixed(4)}</td>
                        <td className={cx.td}>
                          <ExtLink href={x.url}>PDF</ExtLink>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </Panel>
      </div>
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Panel title="Issuer profile" help="Legal entities, jurisdictions and roles, each with its source and as-of date.">
          {!a.issuer ? (
            <ColdState>Issuer: unknown (not curated).</ColdState>
          ) : (
            <>
              <KV k="Issuer">{a.issuer.name}</KV>
              <ul className="mt-2 space-y-2">
                {(a.issuer.entities ?? []).map(e => (
                  <li key={`${e.name}-${e.role}`} className={cx.inset}>
                    <div className={`text-xs font-semibold ${cx.strong}`}>
                      <ExtLink href={e.source_url}>{e.name}</ExtLink>
                    </div>
                    <div className={`mt-0.5 text-[11px] ${cx.text}`}>{e.role}</div>
                    <div className={`mt-0.5 text-[11px] ${cx.muted}`}>
                      {e.jurisdiction} · as of {e.as_of}
                    </div>
                  </li>
                ))}
              </ul>
            </>
          )}
        </Panel>
        <Panel title="Redemption terms" help="Who can redeem with the issuer, minimums and fees, as stated in the issuer's terms (curated, with as-of dates).">
          {a.redemption.length === 0 ? (
            <ColdState>Redemption terms: unknown (not curated).</ColdState>
          ) : (
            <div className="space-y-2">
              {a.redemption.map(r => (
                <div key={r.channel} className={cx.inset}>
                  <div className={`text-xs font-semibold ${cx.strong}`}>
                    <ExtLink href={r.source_url}>{r.channel}</ExtLink>
                  </div>
                  <p className={`mt-1 text-[11px] ${cx.text}`}>{r.eligibility}</p>
                  <p className={`mt-1 text-[11px] ${cx.muted}`}>
                    Minimum: {r.min_native || 'not stated'} · Fee: {r.fee || 'not stated'} · as of {r.as_of}
                  </p>
                </div>
              ))}
            </div>
          )}
        </Panel>
      </div>
    </section>
  );
}

// The v1 regimes are always columns (§7.2); any other curated regime follows.
const V1_REGIMES = ['EU-MiCA', 'US-GENIUS'];

function RegulatoryMatrix({ assets, regimes }: { assets: StableReservesAsset[]; regimes: StableRegime[] }) {
  const names = [...new Set([...V1_REGIMES, ...regimes.map(r => r.regime), ...assets.flatMap(a => a.regulatory.map(r => r.regime))])];
  return (
    <Panel
      title="Regulatory matrix"
      help="Curated status per regime with its as-of date and source; never inferred (§4.5). S11 watches only `not_authorized` under EU MiCA, scoped to EU users. HK, SG and JP are v1.1."
      right={
        <CsvButton
          filename="stablecoin-regulatory.csv"
          label="regulatory matrix"
          headers={['asset', 'regime', 'status', 'entity', 'as_of', 'source_url', 'note']}
          rows={() => assets.flatMap(a => a.regulatory.map(r => [a.asset, r.regime, r.status, r.entity ?? '', r.as_of, r.source_url, r.note ?? '']))}
        />
      }
    >
      <div className="overflow-x-auto">
        <table className={cx.table}>
          <thead>
            <tr>
              <th scope="col" className={cx.th}>
                Asset
              </th>
              {names.map(n => (
                <th key={n} scope="col" className={cx.th}>
                  {REGIME_LABEL[n] ?? n}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {assets.map(a => (
              <tr key={a.asset} className={cx.tr}>
                <th scope="row" className={`${cx.td} font-semibold`}>
                  {a.symbol}
                </th>
                {names.map(n => {
                  const r = a.regulatory.find(x => x.regime === n);
                  return (
                    <td key={n} className={`${cx.tdWrap} align-top`}>
                      {r ? (
                        <div className="space-y-1">
                          <StatusText status={r.status} />
                          <div className={`text-[11px] ${cx.muted}`}>
                            {r.entity ? `${r.entity} · ` : ''}as of {r.as_of} · <ExtLink href={r.source_url}>source</ExtLink>
                          </div>
                          {r.note && <div className={`max-w-[360px] text-[11px] ${cx.muted}`}>{r.note}</div>}
                        </div>
                      ) : (
                        <StatusText status="unknown" />
                      )}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Panel>
  );
}

function KeyDates({ regimes }: { regimes: StableRegime[] }) {
  const dates = regimes.flatMap(r => r.key_dates.map(k => ({ ...k, regime: r.regime }))).sort((x, y) => x.date.localeCompare(y.date));
  return (
    <Panel title="Key dates" help="Statutory and regulatory milestones from the curated regime facts (for example the GENIUS Act's effective-date rule).">
      {dates.length === 0 ? (
        <ColdState>No curated key dates.</ColdState>
      ) : (
        <ol className="flex flex-wrap gap-2">
          {dates.map(k => (
            <li key={`${k.regime}-${k.date}-${k.label}`} className={`${cx.inset} min-w-[200px] max-w-[320px] flex-1`}>
              <div className={`font-mono text-xs font-semibold ${k.past ? cx.muted : cx.strong}`}>
                {k.date} {k.past ? '(past)' : '(upcoming)'}
              </div>
              <div className={`mt-0.5 text-[11px] ${cx.text}`}>
                {REGIME_LABEL[k.regime] ?? k.regime}: {k.source_url ? <ExtLink href={k.source_url}>{k.label}</ExtLink> : k.label}
              </div>
            </li>
          ))}
        </ol>
      )}
    </Panel>
  );
}

export default function ReservesTab({ asset, active, refreshTick }: { asset: StableAsset; active: boolean; refreshTick: number }) {
  const poll = usePolling(() => fetchStableReserves(asset), 60_000, `stable-reserves-${asset}-${refreshTick}`, active);
  const data = poll.data;
  if (!data) {
    return poll.error ? <ErrorNote message={poll.error} /> : <ColdState>Loading reserves…</ColdState>;
  }
  return (
    <div className="space-y-6">
      {poll.error && <ErrorNote message={poll.error} />}
      <p className={`text-[11px] ${cx.muted}`}>
        Curated facts version {data.facts_version}
        {data.override_status !== 'none' && ` · operator override: ${data.override_status}${data.override_version ? ` (${data.override_version})` : ''}`}. Attestations are
        point-in-time reports unless stated otherwise; no yield is paid to holders by the issuers.
      </p>
      {data.assets.map(a => (
        <AssetReserves key={a.asset} a={a} />
      ))}
      <RegulatoryMatrix assets={data.assets} regimes={data.regimes} />
      <KeyDates regimes={data.regimes} />
    </div>
  );
}

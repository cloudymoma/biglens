import { useEffect, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { AlertTriangle, Coins, ExternalLink, Radar, Repeat, ShieldAlert, Snowflake } from 'lucide-react';
import type { AddressRiskSource, ScamRadarResponse } from '../../types';
import { fetchAddressRiskScamRadar } from '../../api';
import { ErrorBanner, MetricCard } from '../../dashboards/shared';
import { AXIS_LABEL, CHART_TOOLTIP, Panel, SPLIT_LINE, fmtNum, shortHash } from './shared';

const ETH_COLOR = '#60a5fa';
const TRON_COLOR = '#34d399';
const FAKE_COLOR = '#a78bfa';
const BTC_COLOR = '#fbbf24';

const RADAR_SOURCES: Array<{ id: string; label: string }> = [
  { id: 'scam_eth', label: 'ETH Poisoning & Fake Tokens (R1/R2)' },
  { id: 'scam_tron', label: 'TRON Lookalike Poisoning (R3)' },
  { id: 'tron_stablecoin_logs', label: 'TRON USDT Freeze Logs (R4)' },
  { id: 'scam_btc', label: 'Bitcoin Explicit RBF (R5)' },
];

function sourceErr(src?: AddressRiskSource): string {
  if (!src) return '';
  if (src.status === 'error') return src.last_error || src.error || 'sync_failed';
  return '';
}

function sourceReady(src?: AddressRiskSource): boolean {
  if (!src) return false;
  if (src.status === 'error' || src.status === 'empty') return false;
  return Boolean(src.cursor);
}

function seriesPoint(src: AddressRiskSource | undefined, day: string, val: number): number | null {
  if (!sourceReady(src)) return null;
  if (src?.coverage_from && day < src.coverage_from) return null;
  if (src?.cursor && day > src.cursor) return null;
  return val;
}

export default function ScamRadarPanel({ onInspect }: { onInspect: (address: string) => void }) {
  const [data, setData] = useState<ScamRadarResponse | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    fetchAddressRiskScamRadar()
      .then(setData)
      .catch(e => {
        const d = e.response?.data;
        setError(typeof d === 'string' && d ? d : e.message);
      });
  }, []);

  if (error) return <ErrorBanner message={`Scam Radar: ${error}`} />;
  if (!data) return <p className="text-xs text-zinc-500">Loading Scam Radar…</p>;

  const cov = data.coverage ?? {};
  const ethSrc = cov.scam_eth;
  const tronSrc = cov.scam_tron;
  const tronFreezeSrc = cov.tron_stablecoin_logs;
  const btcSrc = cov.scam_btc;

  const ethErr = sourceErr(ethSrc);
  const tronErr = sourceErr(tronSrc);
  const tronFreezeErr = sourceErr(tronFreezeSrc);
  const btcErr = sourceErr(btcSrc);

  const ethOk = sourceReady(ethSrc) && Boolean(data.kpis.eth_day);
  const tronOk = sourceReady(tronSrc) && Boolean(data.kpis.tron_day);
  const tronFreezeOk = sourceReady(tronFreezeSrc);
  const btcOk = sourceReady(btcSrc) && data.btc_rbf !== null;

  const erroredSources = RADAR_SOURCES.map(s => ({
    ...s,
    err: sourceErr(cov[s.id]),
  })).filter(s => Boolean(s.err));

  const ethPoisonVal = ethErr ? '—' : ethOk ? fmtNum(data.kpis.eth_poison_hits) : '—';
  const ethPoisonDetail = ethErr
    ? `sync error: ${ethErr}`
    : ethOk
      ? `${fmtNum(data.kpis.eth_poison_victims)} victims · ${data.kpis.eth_day}`
      : 'not synced';

  const tronPoisonVal = tronErr ? '—' : tronOk ? fmtNum(data.kpis.tron_poison_hits) : '—';
  const tronPoisonDetail = tronErr
    ? `sync error: ${tronErr}`
    : tronOk
      ? `${fmtNum(data.kpis.tron_poison_victims)} victims · ${data.kpis.tron_day}${data.kpis.tron_candidates > 0 ? ` · ${fmtNum(data.kpis.tron_candidates)} pairs` : ''}`
      : 'not synced';

  const fakeTransferVal = ethErr ? '—' : ethOk ? fmtNum(data.kpis.eth_fake_transfers) : '—';
  const fakeTransferDetail = ethErr
    ? `sync error: ${ethErr}`
    : ethOk
      ? `${fmtNum(data.kpis.eth_fake_contracts)} contracts · ${data.kpis.eth_day}`
      : 'not synced';

  const tronFreezeVal = tronFreezeErr ? '—' : tronFreezeOk ? fmtNum(data.kpis.tron_usdt_freezes_30d) : '—';
  const tronFreezeDetail = tronFreezeErr
    ? `sync error: ${tronFreezeErr}`
    : tronFreezeOk
      ? `30d to ${tronFreezeSrc?.cursor}`
      : 'not synced';

  const btcRbfVal = btcErr ? '—' : btcOk && data.btc_rbf ? `${data.btc_rbf.percent.toFixed(1)}%` : '—';
  const btcRbfDetail = btcErr
    ? `sync error: ${btcErr}`
    : btcOk && data.btc_rbf
      ? `${fmtNum(data.btc_rbf.rbf_txs)} / ${fmtNum(data.btc_rbf.txs)} txs · ${data.btc_rbf.day}`
      : 'not synced';

  return (
    <Panel
      title="Scam Radar"
      note="daily on-chain scam & settlement risk intelligence · Data: BigQuery public datasets, ~$1/month"
    >
      <div className="space-y-4">
        {erroredSources.length > 0 && (
          <div className="rounded-md border border-amber-500/40 bg-amber-950/30 px-3 py-2 text-xs text-amber-200 flex items-start gap-2">
            <AlertTriangle size={15} className="text-amber-400 shrink-0 mt-0.5" />
            <div className="space-y-0.5">
              <div className="font-medium text-amber-100">
                Scam Radar source sync error — affected metrics show &ldquo;—&rdquo; instead of 0
              </div>
              <ul className="text-[11px] text-amber-200/90 space-y-0.5">
                {erroredSources.map(s => (
                  <li key={s.id} className="font-mono">
                    {s.label} ({s.id}): {s.err}
                  </li>
                ))}
              </ul>
            </div>
          </div>
        )}

        <div className="grid grid-cols-2 lg:grid-cols-5 gap-3">
          <MetricCard
            label="ETH poisoning (1d)"
            value={ethPoisonVal}
            icon={<Radar size={15} />}
            detail={ethPoisonDetail}
            accentColor={ETH_COLOR}
          />
          <MetricCard
            label="TRON poisoning (1d)"
            value={tronPoisonVal}
            icon={<Radar size={15} />}
            detail={tronPoisonDetail}
            accentColor={TRON_COLOR}
          />
          <MetricCard
            label="Fake stablecoin txs (1d)"
            value={fakeTransferVal}
            icon={<Coins size={15} />}
            detail={fakeTransferDetail}
            accentColor={FAKE_COLOR}
          />
          <MetricCard
            label="TRON USDT freezes (30d)"
            value={tronFreezeVal}
            icon={<Snowflake size={15} />}
            detail={tronFreezeDetail}
            accentColor={ETH_COLOR}
          />
          <MetricCard
            label="BTC explicit RBF (1d)"
            value={btcRbfVal}
            icon={<Repeat size={15} />}
            detail={btcRbfDetail}
            accentColor={BTC_COLOR}
          />
        </div>

        {data.empty ? (
          <div className="rounded-md border border-zinc-800 bg-zinc-900/50 p-3 text-xs text-zinc-400 space-y-1.5">
            <p className="text-zinc-200 font-medium">
              Scam Radar has no synced daily snapshots yet (or automatic sync is disabled).
            </p>
            <p>
              When enabled, Scam Radar syncs complete UTC days from BigQuery public datasets: Ethereum &amp; TRON
              lookalike address-poisoning hits (R1/R3), counterfeit USDT/USDC token contracts on Ethereum (R2), TRON
              USDT blacklist events (R4), and Bitcoin explicit opt-in RBF share (R5).
            </p>
            <p className="text-[11px] text-zinc-500">
              Estimated BigQuery on-demand scan: ~160 GB (~$1) for a 30-day cold start, then ~5.3 GB/day (~$1/month).
              Set <code className="font-mono text-zinc-300">address_risk.scam_radar.initial_days: 0</code> in{' '}
              <code className="font-mono text-zinc-300">conf.yaml</code> to disable background syncing.
            </p>
            <p className="text-[11px] text-zinc-500">
              Methodology note: results are a lower bound — same-day pairing only; poisoning transfers without a
              same-day genuine transfer on the victim address are not detected.
            </p>
          </div>
        ) : (
          <>
            {btcOk && data.btc_rbf && (
              <div className="rounded-md border border-zinc-800 bg-zinc-900/60 px-3 py-2 text-xs text-zinc-200 flex items-center gap-2">
                <ShieldAlert size={15} className="text-amber-400 shrink-0" />
                <span>
                  <span className="font-semibold text-white">{data.btc_rbf.percent.toFixed(1)}%</span> of Bitcoin
                  transactions signal RBF — a 0-conf payment can be replaced. Wait for confirmations.
                </span>
              </div>
            )}

            <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
              <div className="rounded-md border border-zinc-800/80 bg-zinc-900/30 p-3">
                <div className="flex items-baseline justify-between mb-2">
                  <span className="text-xs font-medium text-zinc-200">30-day poisoning &amp; fake-token trend</span>
                  <span className="text-[11px] text-zinc-500">complete UTC days · lower bound</span>
                </div>
                <ReactECharts
                  style={{ height: 220 }}
                  option={{
                    tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
                    legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
                    grid: { left: 48, right: 16, top: 32, bottom: 24 },
                    xAxis: {
                      type: 'category',
                      data: data.trend.map(p => p.day.slice(5)),
                      axisLabel: AXIS_LABEL,
                    },
                    yAxis: { type: 'value', minInterval: 1, axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE },
                    series: [
                      {
                        name: 'ETH poisoning hits',
                        type: 'line',
                        showSymbol: false,
                        data: data.trend.map(p => seriesPoint(ethSrc, p.day, p.eth_poison_hits)),
                        lineStyle: { color: ETH_COLOR, width: 2 },
                        itemStyle: { color: ETH_COLOR },
                      },
                      {
                        name: 'TRON poisoning hits',
                        type: 'line',
                        showSymbol: false,
                        data: data.trend.map(p => seriesPoint(tronSrc, p.day, p.tron_poison_hits)),
                        lineStyle: { color: TRON_COLOR, width: 2 },
                        itemStyle: { color: TRON_COLOR },
                      },
                      {
                        name: 'ETH fake token txs',
                        type: 'line',
                        showSymbol: false,
                        data: data.trend.map(p => seriesPoint(ethSrc, p.day, p.eth_fake_transfers)),
                        lineStyle: { color: FAKE_COLOR, width: 1.5, type: 'dashed' },
                        itemStyle: { color: FAKE_COLOR },
                      },
                    ],
                  }}
                />
              </div>

              <div className="rounded-md border border-zinc-800/80 bg-zinc-900/30 p-3">
                <div className="flex items-baseline justify-between mb-2">
                  <span className="text-xs font-medium text-zinc-200">Top fake stablecoin contracts (last 7d, ETH)</span>
                  <span className="text-[11px] text-zinc-500">non-official contracts using USDT/USDC symbols</span>
                </div>
                {ethErr ? (
                  <p className="text-xs text-amber-300 font-mono py-4">
                    ETH fake-token source error: {ethErr}
                  </p>
                ) : data.fake_tokens.length === 0 ? (
                  <p className="text-xs text-zinc-500 py-4">No counterfeit stablecoin contracts recorded in the last 7 days.</p>
                ) : (
                  <div className="overflow-x-auto">
                    <table className="w-full text-xs">
                      <thead className="text-zinc-500 text-left">
                        <tr>
                          <th className="py-1 pr-3 font-normal">Contract</th>
                          <th className="pr-3 font-normal">Symbol</th>
                          <th className="pr-3 font-normal text-right">Transfers</th>
                          <th className="pr-3 font-normal text-right">Recipients</th>
                          <th className="font-normal text-right">Last seen</th>
                        </tr>
                      </thead>
                      <tbody className="text-zinc-300">
                        {data.fake_tokens.map(t => (
                          <tr key={t.contract} className="border-t border-zinc-800/60">
                            <td className="py-1 pr-3">
                              <div className="flex items-center gap-1.5">
                                <button
                                  type="button"
                                  onClick={() => onInspect(t.contract)}
                                  title={t.contract}
                                  className="font-mono text-zinc-200 hover:text-white underline decoration-zinc-700"
                                >
                                  {shortHash(t.contract)}
                                </button>
                                <a
                                  href={t.explorer_url}
                                  target="_blank"
                                  rel="noopener noreferrer"
                                  title="View on Etherscan"
                                  className="text-zinc-500 hover:text-zinc-300"
                                >
                                  <ExternalLink size={11} />
                                </a>
                              </div>
                            </td>
                            <td className="pr-3 font-mono text-zinc-300">{t.symbol}</td>
                            <td className="pr-3 font-mono text-right">{fmtNum(t.transfers)}</td>
                            <td className="pr-3 font-mono text-right">{fmtNum(t.recipients)}</td>
                            <td className="font-mono text-zinc-400 text-right">{t.last_seen}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </div>
            </div>
          </>
        )}

        <div className="border-t border-zinc-800/70 pt-2.5 flex flex-col gap-1.5 text-[11px] text-zinc-500">
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
            {RADAR_SOURCES.map(meta => {
              const s = cov[meta.id];
              const err = sourceErr(s);
              const range = s?.coverage_from && s?.cursor ? `${s.coverage_from} → ${s.cursor}` : 'not synced';
              const lastSync = s?.last_ok_at ? s.last_ok_at.slice(0, 16).replace('T', ' ') : '—';
              return (
                <span key={meta.id} className="font-mono">
                  <span className="text-zinc-400">{meta.id}:</span>{' '}
                  {err ? (
                    <span className="text-amber-400">error ({err})</span>
                  ) : (
                    <span>
                      {range} (synced {lastSync})
                    </span>
                  )}
                </span>
              );
            })}
          </div>
          <div>
            Data: BigQuery public datasets, ~$1/month · Results are a lower bound: same-day pairing only; poisoning
            without a same-day genuine transfer is not detected.
          </div>
        </div>
      </div>
    </Panel>
  );
}

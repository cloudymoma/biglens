import { useEffect, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import { Flame, Landmark, ListX, Snowflake } from 'lucide-react';
import type { AddressRiskOverview as Overview, AddressRiskSources } from '../../types';
import { fetchAddressRiskOverview } from '../../api';
import { ErrorBanner, MetricCard } from '../../dashboards/shared';
import { AXIS_LABEL, CHART_TOOLTIP, Panel, SPLIT_LINE, fmtNum, shortHash } from './shared';
import { BACKFILL_COMMAND, RISK_SOURCE_LABELS, RISK_SOURCE_META } from './addressRiskTools';

// Categorical colors only: red/orange are reserved for severity.
const USDT_COLOR = '#60a5fa';
const USDC_COLOR = '#a78bfa';
const UNFREEZE_COLOR = '#a1a1aa';

const ACTION_LABELS: Record<string, string> = { freeze: 'Freeze', unfreeze: 'Unfreeze', destroy: 'Destroy' };

function fmtCompactUSD(raw: string): string {
  const n = Number(raw);
  if (!Number.isFinite(n)) return `$${raw}`;
  if (n >= 999_995_000) return `$${(n / 1e9).toFixed(2)}b`;
  if (n >= 999_995) return `$${(n / 1e6).toFixed(2)}m`;
  if (n >= 1e3) return `$${(n / 1e3).toFixed(2)}k`;
  return `$${n.toFixed(2)}`;
}

function fmtExactUSD(raw: string): string {
  const [intPart = '0', decPart] = raw.split('.');
  const withCommas = intPart.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  return decPart !== undefined ? `$${withCommas}.${decPart}` : `$${withCommas}`;
}

function BackfillHint({ empty }: { empty: boolean }) {
  return (
    <div className="text-[11px] text-zinc-500 space-y-1">
      <p>
        {empty
          ? 'No USDT/USDC freeze history is stored yet. Run the backfill on the server (full history, ~$20 of BigQuery on-demand cost):'
          : 'Freeze history is partial. For full history run on the server (~$20 of BigQuery on-demand cost):'}
      </p>
      <code className="block rounded-md bg-zinc-900 border border-zinc-800 px-2 py-1 font-mono text-zinc-300 break-all">
        {BACKFILL_COMMAND}
      </code>
      <p>Without --yes it only prints a dry-run cost estimate.</p>
    </div>
  );
}

export default function AddressRiskOverview({ sources, onInspect }: {
  sources: AddressRiskSources | null;
  onInspect: (address: string) => void;
}) {
  const [data, setData] = useState<Overview | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    fetchAddressRiskOverview()
      .then(setData)
      .catch(e => {
        const d = e.response?.data;
        setError(typeof d === 'string' && d ? d : e.message);
      });
  }, []);

  if (error) return <ErrorBanner message={`Overview: ${error}`} />;
  if (!data) return <p className="text-xs text-zinc-500">Loading overview…</p>;

  const since = data.coverage.partial ? `since ${data.coverage.coverage_from}` : 'full history';
  const statusById = new Map((sources?.lists ?? []).map(s => [s.id, s]));
  const entries: Record<string, string> = {
    ofac: fmtNum(data.kpis.ofac_count),
    mew_darklist: fmtNum(data.kpis.mew_darklist_count),
    stablecoin: data.empty ? '—' : `${data.coverage.coverage_from} → ${data.coverage.cursor}`,
  };

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 lg:grid-cols-5 gap-3">
        <MetricCard label="OFAC addresses" value={fmtNum(data.kpis.ofac_count)} icon={<Landmark size={15} />} detail="OFAC-listed addresses (EVM, TRON, Bitcoin)" accentColor="#71717a" />
        <MetricCard label="USDT frozen now" value={data.empty ? '—' : fmtNum(data.kpis.usdt_frozen_count)} icon={<Snowflake size={15} />} detail={data.empty ? 'not synced' : since} accentColor={USDT_COLOR} />
        <MetricCard label="USDC frozen now" value={data.empty ? '—' : fmtNum(data.kpis.usdc_frozen_count)} icon={<Snowflake size={15} />} detail={data.empty ? 'not synced' : since} accentColor={USDC_COLOR} />
        <MetricCard label="USDT destroyed" value={data.empty ? '—' : fmtCompactUSD(data.kpis.usdt_destroyed_total)} valueTitle={data.empty ? undefined : fmtExactUSD(data.kpis.usdt_destroyed_total)} icon={<Flame size={15} />} detail={data.empty ? 'not synced' : `USDT · ${since}`} accentColor={USDT_COLOR} />
        <MetricCard label="MEW darklist" value={fmtNum(data.kpis.mew_darklist_count)} icon={<ListX size={15} />} detail="historical list, frozen since 2020-11" accentColor="#71717a" />
      </div>

      {(data.empty || data.coverage.partial) && (
        <Panel title="Freeze history coverage" note={data.empty ? 'not synced' : `covered since ${data.coverage.coverage_from}`}>
          <BackfillHint empty={data.empty} />
        </Panel>
      )}

      {!data.empty && (
        <Panel title="Stablecoin freezes" note={`per ${data.trend.granularity} · complete UTC days to ${data.coverage.cursor}`}>
          <ReactECharts
            style={{ height: 240 }}
            option={{
              tooltip: { trigger: 'axis', ...CHART_TOOLTIP },
              legend: { textStyle: { color: '#a1a1aa', fontSize: 11 }, top: 0 },
              grid: { left: 40, right: 16, top: 32, bottom: 24 },
              xAxis: { type: 'category', data: data.trend.points.map(p => p.bucket), axisLabel: AXIS_LABEL },
              yAxis: { type: 'value', minInterval: 1, axisLabel: AXIS_LABEL, splitLine: SPLIT_LINE },
              series: [
                { name: 'USDT freeze', type: 'line', showSymbol: false, data: data.trend.points.map(p => p.usdt_freeze), lineStyle: { color: USDT_COLOR, width: 2 }, itemStyle: { color: USDT_COLOR } },
                { name: 'USDC freeze', type: 'line', showSymbol: false, data: data.trend.points.map(p => p.usdc_freeze), lineStyle: { color: USDC_COLOR, width: 2 }, itemStyle: { color: USDC_COLOR } },
                { name: 'Unfreeze', type: 'line', showSymbol: false, data: data.trend.points.map(p => p.unfreeze), lineStyle: { color: UNFREEZE_COLOR, width: 1, type: 'dashed' }, itemStyle: { color: UNFREEZE_COLOR } },
              ],
            }}
          />
        </Panel>
      )}

      {data.recent_events.length > 0 && (
        <Panel title="Latest freeze events" note="click an address to look it up">
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead className="text-zinc-500 text-left">
                <tr><th className="py-1 pr-3 font-normal">Time (UTC)</th><th className="pr-3 font-normal">Token</th><th className="pr-3 font-normal">Action</th><th className="pr-3 font-normal">Address</th><th className="pr-3 font-normal">Amount</th><th className="font-normal">Tx</th></tr>
              </thead>
              <tbody className="text-zinc-300">
                {data.recent_events.map((e, i) => (
                  <tr key={`${e.tx_hash}-${e.token}-${e.action}-${e.address}-${i}`} className="border-t border-zinc-800/60">
                    <td className="py-1 pr-3 font-mono text-zinc-400">{e.block_time.slice(0, 16).replace('T', ' ')}</td>
                    <td className="pr-3">{e.token}</td>
                    <td className="pr-3">{ACTION_LABELS[e.action] ?? e.action}</td>
                    <td className="pr-3">
                      <button type="button" onClick={() => onInspect(e.address)} title={e.address}
                              className="font-mono text-zinc-200 hover:text-white underline decoration-zinc-700">
                        {shortHash(e.address)}
                      </button>
                    </td>
                    <td className="pr-3 font-mono">{e.amount ? `${e.amount} USDT` : ''}</td>
                    <td>
                      <a href={`https://etherscan.io/tx/${e.tx_hash}`} target="_blank" rel="noopener noreferrer"
                         title={e.tx_hash} className="font-mono text-zinc-400 hover:text-zinc-200">{shortHash(e.tx_hash)}</a>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Panel>
      )}

      <Panel title="Sources" note="lists sync in the background; live sources run per lookup">
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead className="text-zinc-500 text-left">
              <tr><th className="py-1 pr-3 font-normal">Source</th><th className="pr-3 font-normal">Entries</th><th className="pr-3 font-normal">Last fetched</th><th className="pr-3 font-normal">Upstream changed</th><th className="pr-3 font-normal">Status</th><th className="pr-3 font-normal">Last error</th><th className="pr-3 font-normal">Known delay</th><th className="font-normal">License / terms</th></tr>
            </thead>
            <tbody className="text-zinc-300">
              {RISK_SOURCE_META.map(m => {
                const s = statusById.get(m.id);
                return (
                  <tr key={m.id} className="border-t border-zinc-800/60 align-top">
                    <td className="py-1 pr-3">
                      <a href={m.link} target="_blank" rel="noopener noreferrer" className="hover:text-white">{RISK_SOURCE_LABELS[m.id] ?? m.id}</a>
                    </td>
                    <td className="pr-3 font-mono">{entries[m.id] ?? '—'}</td>
                    <td className="pr-3 font-mono text-zinc-400">{s?.last_ok_at ? s.last_ok_at.slice(0, 16).replace('T', ' ') : '—'}</td>
                    <td className="pr-3 font-mono text-zinc-400">{s?.upstream_changed_at ? s.upstream_changed_at.slice(0, 10) : '—'}</td>
                    <td className="pr-3">
                      {s ? s.status.replace('_', ' ') : m.id === 'etherscan' && !sources?.etherscan?.configured ? 'not configured' : 'live'}
                    </td>
                    <td className="pr-3 text-zinc-400">{s?.last_error || s?.error || ''}</td>
                    <td className="pr-3 text-zinc-400">{m.delay}</td>
                    <td className="text-zinc-400">{m.terms}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </Panel>
    </div>
  );
}

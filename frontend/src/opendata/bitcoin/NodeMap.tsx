import { useEffect, useMemo, useState } from 'react';
import ReactECharts from 'echarts-for-react';
import * as echarts from 'echarts';
import type { BtcNodeCountry } from './types';
import { CHART_TOOLTIP } from '../crypto/shared';
import { escapeHtml, fmtPct } from './format';

interface NodeMapProps {
  countries: BtcNodeCountry[];
  clearnetTotal: number;
}

const GEO_NAME_OVERRIDES: Record<string, string> = {
  US: 'United States of America',
  GB: 'United Kingdom',
  CZ: 'Czechia',
  KR: 'South Korea',
  RU: 'Russia',
  TW: 'Taiwan',
  VN: 'Vietnam',
  IR: 'Iran',
  VE: 'Venezuela',
  BO: 'Bolivia',
  TZ: 'United Republic of Tanzania',
  SY: 'Syria',
  MD: 'Moldova',
  LA: 'Laos',
  CD: 'Democratic Republic of the Congo',
  CG: 'Republic of the Congo',
  CI: 'Ivory Coast',
  BA: 'Bosnia and Herzegovina',
  MK: 'Macedonia',
};

export default function NodeMap({ countries, clearnetTotal }: NodeMapProps) {
  const [mapReady, setMapReady] = useState<boolean>(() => Boolean(echarts.getMap('world')));

  useEffect(() => {
    if (mapReady) return;
    let cancelled = false;
    fetch('/maps/world.geo.json')
      .then(r => r.json())
      .then(geoJson => {
        if (cancelled) return;
        echarts.registerMap('world', geoJson);
        setMapReady(true);
      })
      .catch(() => {
        if (!cancelled) setMapReady(false);
      });
    return () => {
      cancelled = true;
    };
  }, [mapReady]);

  const mapSeriesData = useMemo(
    () =>
      countries.map(c => ({
        name: GEO_NAME_OVERRIDES[c.code] ?? c.name,
        value: c.count,
        code: c.code,
        sharePct: c.share_pct,
      })),
    [countries],
  );

  const maxCount = useMemo(
    () => Math.max(10, ...countries.map(c => c.count)),
    [countries],
  );

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
      <div className="lg:col-span-2">
        {mapReady ? (
          <ReactECharts
            style={{ height: 280 }}
            option={{
              tooltip: {
                trigger: 'item',
                ...CHART_TOOLTIP,
                formatter: (p: { name?: string; value?: number; data?: { code?: string; sharePct?: number } }) => {
                  const val = typeof p.value === 'number' && !Number.isNaN(p.value) ? p.value : 0;
                  const share = p.data?.sharePct ?? (clearnetTotal > 0 ? (val / clearnetTotal) * 100 : 0);
                  return `<div class="text-xs"><div class="font-semibold">${escapeHtml(p.name ?? 'Unknown')}</div><div>${val.toLocaleString()} clearnet nodes (${share.toFixed(1)}%)</div></div>`;
                },
              },
              visualMap: {
                min: 0,
                max: maxCount,
                left: 12,
                bottom: 12,
                text: ['High', '0'],
                calculable: false,
                inRange: {
                  color: ['#18181b', '#78350f', '#d97706', '#f59e0b', '#fbbf24'],
                },
                textStyle: { color: '#a1a1aa', fontSize: 10 },
              },
              series: [
                {
                  name: 'Clearnet Nodes',
                  type: 'map',
                  map: 'world',
                  roam: true,
                  emphasis: {
                    label: { show: false },
                    itemStyle: { areaColor: '#f59e0b' },
                  },
                  itemStyle: {
                    areaColor: '#18181b',
                    borderColor: '#27272a',
                    borderWidth: 0.6,
                  },
                  data: mapSeriesData,
                },
              ],
            }}
          />
        ) : (
          <div className="flex h-[280px] items-center justify-center rounded-lg border border-zinc-800/60 bg-zinc-900/40 text-xs text-zinc-500">
            Loading world map geometry…
          </div>
        )}
      </div>

      <div className="max-h-[280px] overflow-y-auto rounded-lg border border-zinc-800/60 bg-zinc-950/50 p-3">
        <div className="mb-2 flex items-center justify-between text-[11px] font-medium text-zinc-400">
          <span>Top Countries (Clearnet)</span>
          <span>{clearnetTotal.toLocaleString()} nodes</span>
        </div>
        <table className="w-full text-xs">
          <thead>
            <tr className="border-b border-zinc-800/60 text-zinc-500">
              <th className="py-1 text-left font-medium">Country</th>
              <th className="py-1 text-right font-medium">Nodes</th>
              <th className="py-1 text-right font-medium">Share</th>
            </tr>
          </thead>
          <tbody>
            {countries.slice(0, 12).map(c => (
              <tr key={c.code} className="border-b border-zinc-800/30">
                <td className="py-1.5 text-zinc-200">
                  <span className="mr-1.5 font-mono text-[10px] text-zinc-500">{c.code}</span>
                  {c.name}
                </td>
                <td className="py-1.5 text-right font-mono text-zinc-300">{c.count.toLocaleString()}</td>
                <td className="py-1.5 text-right font-mono text-amber-300">{fmtPct(c.share_pct, 1)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

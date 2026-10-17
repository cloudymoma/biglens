import { useState } from 'react';
import { Bitcoin, RefreshCw } from 'lucide-react';
import { fetchBitcoinLive, fetchBitcoinMeta } from './api';
import { usePolling } from './usePolling';
import { useBitcoinBigQuery } from './useBitcoinBigQuery';
import BitcoinTicker from './BitcoinTicker';
import BigQuerySwitch from './BigQuerySwitch';
import BigQueryStatusChip from './BigQueryStatusChip';
import SourceBadge from './SourceBadge';

const WINDOWS = [
  { label: '30D', days: 30 },
  { label: '90D', days: 90 },
  { label: '180D', days: 180 },
  { label: '1Y', days: 365 },
] as const;

export default function BitcoinDashboard() {
  const [days, setDays] = useState<number>(90);
  const [refreshTick, setRefreshTick] = useState<number>(0);

  const metaPoll = usePolling(fetchBitcoinMeta, 60_000, `btc-meta-${refreshTick}`);
  const livePoll = usePolling(fetchBitcoinLive, 30_000, `btc-live-${refreshTick}`);

  const bqControl = useBitcoinBigQuery(metaPoll.data);
  const bigquery = metaPoll.data?.bigquery ?? null;

  return (
    <div className="space-y-6 p-6 text-zinc-100">
      {/* Header */}
      <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
        <div className="space-y-1">
          <div className="flex items-center gap-2.5">
            <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-amber-500/15 text-amber-400">
              <Bitcoin className="h-5 w-5" />
            </div>
            <div>
              <h1 className="text-xl font-bold tracking-tight text-zinc-100">
                Bitcoin Intelligence
              </h1>
              <p className="text-xs text-zinc-400">
                Real-time mempool &amp; market telemetry paired with verifiable BigQuery on-chain accounting
              </p>
            </div>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2.5">
          <BigQueryStatusChip bigquery={bigquery} />
          <BigQuerySwitch control={bqControl} />
          <div className="inline-flex rounded-lg border border-zinc-800 bg-zinc-900/80 p-0.5 text-xs">
            {WINDOWS.map(w => (
              <button
                key={w.days}
                type="button"
                onClick={() => setDays(w.days)}
                className={`rounded-md px-2.5 py-1 font-medium transition ${
                  days === w.days
                    ? 'bg-amber-500 text-zinc-950'
                    : 'text-zinc-400 hover:text-zinc-200'
                }`}
              >
                {w.label}
              </button>
            ))}
          </div>
          <button
            type="button"
            onClick={() => setRefreshTick(t => t + 1)}
            title="Refresh live telemetry"
            className="inline-flex items-center gap-1.5 rounded-lg border border-zinc-800 bg-zinc-900/80 px-2.5 py-1.5 text-xs text-zinc-300 transition hover:border-zinc-700 hover:text-zinc-100"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            <span>Refresh</span>
          </button>
        </div>
      </div>

      {/* Live Ticker Strip */}
      <BitcoinTicker live={livePoll.data} loading={livePoll.loading} />

      {/* Main Content Area (tabs added in M2/M3) */}
      <div className="rounded-xl border border-zinc-800/80 bg-zinc-900/40 p-6 text-xs text-zinc-400">
        Selected window: {days} days · BigQuery panels: {bqControl.show ? 'Visible' : 'Hidden'}
      </div>

      {/* Sources Footer */}
      {livePoll.data?.sources && livePoll.data.sources.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 border-t border-zinc-800/70 pt-3">
          <span className="text-[11px] font-medium text-zinc-500">Live sources:</span>
          {livePoll.data.sources.map(s => (
            <SourceBadge key={s.id} source={s} />
          ))}
        </div>
      )}
    </div>
  );
}

import { Database, Eye, Terminal } from 'lucide-react';
import type { BtcMetaBigQuery } from './types';

export default function BigQueryLocked({
  title,
  description,
  bigquery,
  onEnableDisplay,
}: {
  title: string;
  description: string;
  bigquery?: BtcMetaBigQuery | null;
  onEnableDisplay?: () => void;
}) {
  const canDisplay = Boolean(bigquery?.jobs || bigquery?.data_available);
  const firstDay = bigquery?.costs?.first_day_usd ?? 1.41;
  const steadyMonth = bigquery?.costs?.steady_month_usd ?? 3.62;

  return (
    <div className="rounded-xl border border-dashed border-zinc-800 bg-zinc-900/40 p-6">
      <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
        <div className="space-y-2">
          <div className="inline-flex items-center gap-2 rounded-full border border-amber-500/30 bg-amber-500/10 px-2.5 py-0.5 text-xs font-medium text-amber-300">
            <Database className="h-3.5 w-3.5" />
            <span>BigQuery On-Chain Layer</span>
          </div>
          <h3 className="text-base font-semibold text-zinc-100">{title}</h3>
          <p className="max-w-2xl text-xs leading-relaxed text-zinc-400">{description}</p>
        </div>

        {canDisplay && onEnableDisplay ? (
          <button
            type="button"
            onClick={onEnableDisplay}
            className="inline-flex shrink-0 items-center gap-2 rounded-lg bg-amber-500 px-3.5 py-2 text-xs font-semibold text-zinc-950 transition hover:bg-amber-400"
          >
            <Eye className="h-3.5 w-3.5" />
            <span>Show BigQuery panels ($0 read)</span>
          </button>
        ) : null}
      </div>

      {!canDisplay && (
        <div className="mt-4 grid gap-3 rounded-lg border border-zinc-800/80 bg-zinc-950/60 p-4 text-xs text-zinc-400 md:grid-cols-2">
          <div className="space-y-1.5">
            <div className="flex items-center gap-1.5 font-medium text-zinc-200">
              <Terminal className="h-3.5 w-3.5 text-amber-400" />
              <span>Operator opt-in required (conf.yaml)</span>
            </div>
            <p className="text-[11px] leading-relaxed text-zinc-400">
              BigQuery background jobs are disabled by default so visitors can never bill the operator&apos;s project.
              Estimated query cost: ~${firstDay.toFixed(2)} first-day backfill, ~${steadyMonth.toFixed(2)}/month steady state.
            </p>
          </div>
          <pre className="overflow-x-auto rounded bg-zinc-900 p-2.5 font-mono text-[11px] text-zinc-300">
{`bitcoin:
  bigquery:
    jobs: true
    coin_age_cadence_days: 7
    monthly_budget_gib: 1024`}
          </pre>
        </div>
      )}
    </div>
  );
}

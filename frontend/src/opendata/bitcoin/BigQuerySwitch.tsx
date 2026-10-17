import { Database } from 'lucide-react';
import type { BitcoinBigQueryControl } from './useBitcoinBigQuery';

export default function BigQuerySwitch({
  control,
}: {
  control: BitcoinBigQueryControl;
}) {
  const { show, available, jobsEnabled, reason, toggle } = control;

  const title = !available
    ? reason
    : 'Browser display preference only — reads pre-synced SQLite tables and never triggers a BigQuery query.';

  return (
    <div
      title={title}
      className="inline-flex items-center gap-2 rounded-lg border border-zinc-800 bg-zinc-900/80 px-3 py-1.5 text-xs"
    >
      <Database className={`h-3.5 w-3.5 ${show ? 'text-amber-400' : 'text-zinc-500'}`} />
      <span className="font-medium text-zinc-300">Show BigQuery panels</span>
      <button
        type="button"
        role="switch"
        aria-checked={show}
        aria-label="Show BigQuery panels"
        disabled={!available}
        onClick={toggle}
        className={`relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors ${
          !available
            ? 'cursor-not-allowed bg-zinc-800 opacity-50'
            : show
              ? 'cursor-pointer bg-amber-500'
              : 'cursor-pointer bg-zinc-700'
        }`}
      >
        <span
          className={`inline-block h-3.5 w-3.5 transform rounded-full bg-white transition-transform ${
            show ? 'translate-x-4' : 'translate-x-1'
          }`}
        />
      </button>
      <span
        className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${
          jobsEnabled
            ? 'bg-emerald-500/15 text-emerald-300'
            : available
              ? 'bg-sky-500/15 text-sky-300'
              : 'bg-zinc-800 text-zinc-400'
        }`}
      >
        {jobsEnabled ? 'Jobs: ON' : available ? 'Cached SQLite' : 'Jobs: OFF'}
      </span>
    </div>
  );
}

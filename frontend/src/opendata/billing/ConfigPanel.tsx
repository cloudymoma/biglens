import { useState } from 'react';
import { postBillingConfig } from '../../api';
import type { BillingDatasetInfo } from '../../types';

interface ConfigPanelProps {
  datasets: BillingDatasetInfo[];
  onChanged: (datasets: BillingDatasetInfo[]) => void;
}

// Add/remove billing export datasets. The backend validates format,
// probes access, and requires a standard export table before persisting.
export default function ConfigPanel({ datasets, onChanged }: ConfigPanelProps) {
  const [input, setInput] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const submit = async (action: 'add' | 'remove', dataset: string) => {
    setBusy(true);
    setError('');
    try {
      const resp = await postBillingConfig(action, dataset);
      onChanged(resp.datasets);
      if (action === 'add') setInput('');
    } catch (e: unknown) {
      const err = e as { response?: { data?: string }; message?: string };
      setError(err.response?.data || err.message || 'request failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-3">
      <div className="flex gap-2">
        <input
          value={input}
          onChange={e => setInput(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter' && input.trim()) submit('add', input.trim()); }}
          placeholder="my-project.billing_dataset"
          className="flex-1 bg-zinc-900 border border-zinc-700 rounded-lg px-3 py-1.5 text-sm text-zinc-200 placeholder-zinc-600"
        />
        <button
          onClick={() => submit('add', input.trim())}
          disabled={busy || !input.trim()}
          className="px-3 py-1.5 rounded-lg text-xs font-medium bg-emerald-700 text-white disabled:opacity-40"
        >
          {busy ? 'Checking…' : 'Add dataset'}
        </button>
      </div>
      {error && <div className="text-xs text-red-400 whitespace-pre-wrap">{error}</div>}
      <ul className="space-y-1">
        {datasets.map(d => (
          <li key={d.dataset} className="flex items-center justify-between text-sm text-zinc-300 bg-zinc-900/60 border border-zinc-800/60 rounded-lg px-3 py-1.5">
            <span>
              <code>{d.dataset}</code>
              {d.error ? (
                <span className="ml-2 text-xs text-red-400">{d.error}</span>
              ) : (
                <span className="ml-2 text-xs text-zinc-500">
                  {d.billing_accounts.length} account(s)
                  {!d.has_resource && ' · no resource export'}
                  {!d.has_pricing && ' · no pricing export'}
                </span>
              )}
            </span>
            <button onClick={() => submit('remove', d.dataset)} disabled={busy} className="text-xs text-zinc-500 hover:text-red-400">
              Remove
            </button>
          </li>
        ))}
      </ul>
      <p className="text-xs text-zinc-500">
        Point BigLens at a BigQuery dataset holding your Cloud Billing export tables
        (gcp_billing_export_v1_*, optionally gcp_billing_export_resource_v1_* and cloud_pricing_export).
      </p>
    </div>
  );
}

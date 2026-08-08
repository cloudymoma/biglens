import { useState } from 'react';
import { postResourcesConfig } from '../api';
import type { ResConfigResponse, ResProjectInfo } from '../types';

interface Props {
  projects: ResProjectInfo[];
  onChanged: (resp: ResConfigResponse) => void;
}

export default function ConfigPanel({ projects, onChanged }: Props) {
  const [value, setValue] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const run = (action: 'add' | 'remove', project: string) => {
    setBusy(true);
    setError('');
    postResourcesConfig(action, project)
      .then(resp => {
        onChanged(resp);
        if (action === 'add') setValue('');
      })
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setBusy(false));
  };

  return (
    <div className="space-y-4">
      <p className="text-xs text-zinc-500">
        Add GCP project IDs to inventory. The BigLens principal needs{' '}
        <code className="text-zinc-300">roles/viewer</code> and{' '}
        <code className="text-zinc-300">roles/cloudasset.viewer</code> on each project.
      </p>
      <div className="flex gap-2">
        <input
          value={value}
          onChange={e => setValue(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter' && value.trim()) run('add', value.trim()); }}
          placeholder="my-project-id"
          className="flex-1 bg-zinc-900 border border-zinc-700 rounded-lg px-3 py-1.5 text-sm text-zinc-200"
        />
        <button
          disabled={busy || !value.trim()}
          onClick={() => run('add', value.trim())}
          className="px-3 py-1.5 rounded-lg text-sm bg-sky-600/80 hover:bg-sky-600 text-white disabled:opacity-40"
        >
          Add project
        </button>
      </div>
      {error && <div className="text-xs text-rose-300 border border-rose-900/60 bg-rose-950/30 rounded-lg p-2">{error}</div>}
      <ul className="space-y-2">
        {projects.map(p => (
          <li key={p.project} className="flex items-center justify-between border border-zinc-800/60 rounded-lg px-3 py-2">
            <div>
              <span className="text-sm text-zinc-200 font-mono">{p.project}</span>
              {p.error && <p className="text-[11px] text-amber-300 mt-1">{p.error}</p>}
            </div>
            <button
              disabled={busy}
              onClick={() => run('remove', p.project)}
              className="text-xs text-zinc-400 hover:text-rose-300"
            >
              Remove
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

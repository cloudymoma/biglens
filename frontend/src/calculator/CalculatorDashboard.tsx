import { useEffect, useState } from 'react';
import { HardDrive, Cpu } from 'lucide-react';
import { fetchCalculatorPresets } from '../api';
import type { CalculatorPresets } from '../types';
import StorageCalculator from './StorageCalculator';
import SlotsCalculator from './SlotsCalculator';

const TABS = [
  { id: 'storage', label: 'Storage', icon: <HardDrive size={14} /> },
  { id: 'slots', label: 'Slots', icon: <Cpu size={14} /> },
] as const;

type TabId = (typeof TABS)[number]['id'];

export default function CalculatorDashboard() {
  const [active, setActive] = useState<TabId>('storage');
  const [visited, setVisited] = useState<ReadonlySet<TabId>>(new Set<TabId>(['storage']));
  const [presets, setPresets] = useState<CalculatorPresets | null>(null);
  const [error, setError] = useState<string | null>(null);

  // List prices live in the backend; every tab needs them before it can
  // seed its rate fields.
  useEffect(() => {
    fetchCalculatorPresets().then(setPresets).catch(err => setError(err instanceof Error ? err.message : String(err)));
  }, []);

  const select = (id: TabId) => {
    setActive(id);
    setVisited(prev => new Set(prev).add(id));
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-1 border-b border-zinc-800/60 pb-2">
        {TABS.map(t => (
          <button
            key={t.id}
            onClick={() => select(t.id)}
            className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer ${
              active === t.id ? 'bg-zinc-800 text-white' : 'text-zinc-500 hover:text-zinc-300'
            }`}
          >
            <span className={active === t.id ? 'text-cyan-400' : ''}>{t.icon}</span>
            {t.label}
          </button>
        ))}
      </div>
      {error && (
        <div className="rounded-xl border border-rose-500/30 bg-rose-500/5 px-4 py-3 text-xs text-rose-300">
          Could not load list prices from the backend: {error}
        </div>
      )}
      {!presets && !error && <p className="text-xs text-zinc-600 px-1">Loading list prices…</p>}
      {/* Once visited a tab stays mounted, so switching preserves what the user typed. */}
      {presets && visited.has('storage') && <div className={active === 'storage' ? '' : 'hidden'}><StorageCalculator presets={presets} /></div>}
      {presets && visited.has('slots') && <div className={active === 'slots' ? '' : 'hidden'}><SlotsCalculator presets={presets} /></div>}
    </div>
  );
}

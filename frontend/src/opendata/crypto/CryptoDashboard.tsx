import { useState } from 'react';
import type React from 'react';
import PulseTab from './PulseTab';
import FeesTab from './FeesTab';

const TABS = [
  { id: 'pulse', label: 'Network Pulse' },
  { id: 'fees', label: 'Fee Market' },
  { id: 'whales', label: 'Whales & Flow' },
  { id: 'tokens', label: 'Token Economy' },
] as const;

type TabId = (typeof TABS)[number]['id'];

// Fees/Whales/Tokens are delivered by later tasks; until then their tabs
// render the placeholder below and the tab bar is already final.
function ComingSoon({ label }: { label: string }) {
  return <p className="text-xs text-zinc-600 py-16 text-center">{label} is landing in a follow-up task.</p>;
}

export default function CryptoDashboard() {
  const [active, setActive] = useState<TabId>('pulse');
  const [visited, setVisited] = useState<ReadonlySet<TabId>>(new Set<TabId>(['pulse']));

  const select = (id: TabId) => {
    setActive(id);
    setVisited(prev => new Set(prev).add(id));
  };

  const tabBody: Record<TabId, React.ReactNode> = {
    pulse: <PulseTab />,
    fees: <FeesTab />,
    whales: <ComingSoon label="Whales & Flow" />,
    tokens: <ComingSoon label="Token Economy" />,
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-1 border-b border-zinc-800/60 pb-2">
        {TABS.map(t => (
          <button
            key={t.id}
            onClick={() => select(t.id)}
            className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors ${
              active === t.id ? 'bg-zinc-800 text-white' : 'text-zinc-500 hover:text-zinc-300'
            }`}
          >
            {t.label}
          </button>
        ))}
      </div>
      {TABS.map(t =>
        visited.has(t.id) ? (
          <div key={t.id} className={active === t.id ? '' : 'hidden'}>
            {tabBody[t.id]}
          </div>
        ) : null,
      )}
    </div>
  );
}

import { useState } from 'react';
import type React from 'react';
import PulseTab from './PulseTab';
import FeesTab from './FeesTab';
import WhalesTab from './WhalesTab';
import TokensTab from './TokensTab';
import MiningTab from './MiningTab';
import AddressRiskTab from './AddressRiskTab';

const TABS = [
  { id: 'pulse', label: 'Network Pulse' },
  { id: 'fees', label: 'Fee Market' },
  { id: 'whales', label: 'Whales & Flow' },
  { id: 'tokens', label: 'Token Economy' },
  { id: 'mining', label: 'Mining Economics' },
  { id: 'risk', label: 'Address Risk' },
] as const;

type TabId = (typeof TABS)[number]['id'];

export default function CryptoDashboard() {
  const [active, setActive] = useState<TabId>('pulse');
  const [visited, setVisited] = useState<ReadonlySet<TabId>>(new Set<TabId>(['pulse']));

  // Whales → Address Risk hand-off. seq changes on every click, so clicking
  // the same address again re-runs the lookup in the already-mounted tab.
  const [inspect, setInspect] = useState({ address: '', seq: 0 });

  const select = (id: TabId) => {
    setActive(id);
    setVisited(prev => new Set(prev).add(id));
  };

  const inspectAddress = (address: string) => {
    select('risk');
    setInspect(prev => ({ address, seq: prev.seq + 1 }));
  };

  const tabBody: Record<TabId, React.ReactNode> = {
    pulse: <PulseTab />,
    fees: <FeesTab />,
    whales: <WhalesTab onInspect={inspectAddress} />,
    tokens: <TokensTab />,
    mining: <MiningTab />,
    risk: <AddressRiskTab inspect={inspect} />,
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

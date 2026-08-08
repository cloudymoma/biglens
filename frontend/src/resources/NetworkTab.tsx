import { useEffect, useState } from 'react';
import { fetchResourcesNetwork } from '../api';
import type { ResNetworkData } from '../types';
import { EmptyState, ErrorBanner } from '../dashboards/shared';
import { Panel, th, td, type ResTabProps } from './shared';

export default function NetworkTab({ project, refreshKey }: ResTabProps) {
  const [data, setData] = useState<ResNetworkData | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setData(null);
    setError('');
    fetchResourcesNetwork(project, refreshKey > 0)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message));
  }, [project, refreshKey]);

  if (error) return <ErrorBanner message={error} />;
  if (!data) return <EmptyState text="Loading network inventory…" />;

  const isOpen = (f: ResNetworkData['firewalls'][number]) =>
    !f.disabled && f.direction === 'INGRESS' && (f.source_ranges ?? []).includes('0.0.0.0/0');

  return (
    <div className="space-y-4">
      <Panel title={`VPC networks (${data.networks.length})`} note={`fetched ${data.fetched_at}`}>
        {data.networks.map(n => (
          <details key={n.name} className="border-t border-zinc-800/40 py-2">
            <summary className="text-sm text-zinc-200 cursor-pointer">
              {n.name}
              {n.auto_create && <span className="ml-2 text-[10px] text-zinc-500">auto subnets</span>}
            </summary>
            <table className="w-full text-sm mt-2">
              <thead><tr><th className={th}>Subnet</th><th className={th}>Region</th><th className={th}>CIDR</th><th className={th}>Private Google access</th></tr></thead>
              <tbody>
                {data.subnets.filter(s => s.network === n.name).map(s => (
                  <tr key={`${s.region}/${s.name}`} className="border-t border-zinc-800/40">
                    <td className={td}>{s.name}</td>
                    <td className={td}>{s.region}</td>
                    <td className={td}>{s.cidr}</td>
                    <td className={td}>{s.private_google_access ? 'on' : 'off'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </details>
        ))}
      </Panel>
      <Panel title={`Firewall rules (${data.firewalls.length})`} note="internet-open ingress rules highlighted">
        <table className="w-full text-sm">
          <thead><tr><th className={th}>Name</th><th className={th}>Network</th><th className={th}>Direction</th><th className={th}>Sources</th><th className={th}>Allows</th><th className={th}>Target tags</th><th className={th}>State</th></tr></thead>
          <tbody>
            {data.firewalls.map(f => (
              <tr key={f.name} className={`border-t border-zinc-800/40 ${isOpen(f) ? 'bg-rose-950/20' : ''}`}>
                <td className={`${td} ${isOpen(f) ? 'text-rose-300' : ''}`}>{f.name}</td>
                <td className={td}>{f.network}</td>
                <td className={td}>{f.direction}</td>
                <td className={td}>{(f.source_ranges ?? []).join(', ')}</td>
                <td className={td}>{(f.allowed ?? []).join(' ')}</td>
                <td className={td}>{(f.target_tags ?? []).join(', ')}</td>
                <td className={td}>{f.disabled ? 'disabled' : 'enabled'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
      <div className="grid md:grid-cols-2 gap-4">
        <Panel title={`IP addresses (${data.addresses.length})`}>
          <table className="w-full text-sm">
            <thead><tr><th className={th}>Name</th><th className={th}>Address</th><th className={th}>Region</th><th className={th}>Type</th><th className={th}>Status</th></tr></thead>
            <tbody>
              {data.addresses.map(a => (
                <tr key={`${a.region}/${a.name}`} className="border-t border-zinc-800/40">
                  <td className={td}>{a.name}</td>
                  <td className={td}>{a.address}</td>
                  <td className={td}>{a.region}</td>
                  <td className={td}>{a.type}</td>
                  <td className={`${td} ${a.status === 'RESERVED' ? 'text-amber-300' : ''}`}>{a.status}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Panel>
        <Panel title={`Load balancers / forwarding rules (${data.forwarding_rules.length})`}>
          <table className="w-full text-sm">
            <thead><tr><th className={th}>Name</th><th className={th}>Scheme</th><th className={th}>IP</th><th className={th}>Ports</th><th className={th}>Target</th></tr></thead>
            <tbody>
              {data.forwarding_rules.map(fr => (
                <tr key={`${fr.region}/${fr.name}`} className="border-t border-zinc-800/40">
                  <td className={td}>{fr.name}</td>
                  <td className={td}>{fr.scheme}</td>
                  <td className={td}>{fr.ip_address}</td>
                  <td className={td}>{fr.ports}</td>
                  <td className={td}>{fr.target}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Panel>
      </div>
    </div>
  );
}

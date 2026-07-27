import { useState, useEffect } from 'react';
import { AlertTriangle, CheckCircle2, Users, Bot, Building2, Key, Lock, EyeOff, Database, Tag, Info } from 'lucide-react';
import type { SecurityDashboardData } from '../types';
import { fetchSecurityDashboard } from '../api';
import { MetricCard, EmptyState, ErrorBanner } from './shared';

interface Props {
  region: string;
  timeRange: string;
}

export default function SecurityPosture({ region, timeRange }: Props) {
  const [data, setData] = useState<SecurityDashboardData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    setError('');
    fetchSecurityDashboard(region, timeRange)
      .then(setData)
      .catch(e => setError(e.response?.data || e.message))
      .finally(() => setLoading(false));
  }, [region, timeRange]);

  if (loading) return <LoadingPulse />;
  if (error) return <ErrorBanner message={error} />;
  if (!data) return null;

  const publicFlags = data.public_flags || [];
  const principals = data.principals || [];
  const unusedGrants = data.unused_grants || [];
  const projectBindings = data.project_bindings || [];
  const datasetPosture = data.dataset_posture || [];
  const rlsPolicies = data.rls_policies || [];
  const sensitiveColumns = data.sensitive_columns || [];
  const tagBypassers = data.tag_bypassers || [];
  const untaggedSensitive = sensitiveColumns.filter(c => !c.tagged);
  const cmekDatasets = datasetPosture.filter(d => d.cmek).length;
  const cmekPct = datasetPosture.length > 0 ? ((cmekDatasets / datasetPosture.length) * 100).toFixed(0) : '0';
  const noExpiration = datasetPosture.filter(d => d.default_exp_days === 0).length;

  return (
    <div className="space-y-6">
      {/* Section 1: Exposure alerts */}
      {publicFlags.length > 0 ? (
        <div className="space-y-3">
          {publicFlags.map((flag, i) => (
            <div
              key={i}
              className="rounded-2xl border border-rose-500/30 p-4"
              style={{ background: 'rgba(251, 113, 133, 0.05)' }}
            >
              <div className="flex items-start gap-3">
                <AlertTriangle size={18} className="text-rose-400 shrink-0 mt-0.5" />
                <div className="flex-1">
                  <p className="text-sm text-white font-medium">
                    <span className="font-mono">{flag.dataset}</span> grants <span className="font-mono text-rose-400">{flag.role}</span> to <span className="font-mono">{flag.grantee}</span>
                  </p>
                  <div className="flex items-center gap-2 mt-2">
                    <KindChip kind={flag.kind} />
                  </div>
                </div>
              </div>
            </div>
          ))}
        </div>
      ) : (
        <div
          className="rounded-2xl border border-emerald-500/30 p-4"
          style={{ background: 'rgba(74, 222, 128, 0.05)' }}
        >
          <div className="flex items-start gap-3">
            <CheckCircle2 size={18} className="text-emerald-400 shrink-0 mt-0.5" />
            <p className="text-sm text-emerald-400">
              No public, domain-wide, or special-group grants detected across scanned datasets.
            </p>
          </div>
        </div>
      )}

      {/* Section 2: Scan coverage note */}
      {data.datasets_scanned < data.datasets_total && (
        <p className="text-xs text-zinc-500">
          Posture scanned first {data.datasets_scanned} of {data.datasets_total} datasets (alphabetical cap).
        </p>
      )}

      {/* Section 3: Project-level access */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Project-Level Access</h3>
        <p className="text-xs text-zinc-500 mb-4">BigQuery & Catalog roles only</p>

        {data.project_iam_error ? (
          <div
            className="rounded-lg border border-amber-500/20 p-3"
            style={{ background: 'rgba(251, 191, 36, 0.05)' }}
          >
            <div className="flex items-start gap-2">
              <Info size={14} className="text-amber-400 shrink-0 mt-0.5" />
              <p className="text-xs text-amber-400">{data.project_iam_error}</p>
            </div>
          </div>
        ) : projectBindings.length > 0 ? (
          <div className="space-y-4">
            {projectBindings.map((binding, i) => (
              <div key={i}>
                <div className="flex items-center gap-2 mb-2">
                  <span className="text-xs font-mono text-white">{binding.role}</span>
                  {binding.basic && (
                    <span className="text-[10px] px-2 py-0.5 rounded-full font-medium border bg-amber-500/10 text-amber-400 border-amber-500/20">
                      BROAD BASIC ROLE
                    </span>
                  )}
                </div>
                <div className="flex flex-wrap gap-2">
                  {binding.members.map((member, j) => (
                    <MemberChip key={j} member={member} />
                  ))}
                </div>
              </div>
            ))}
          </div>
        ) : (
          <EmptyState text="No project-level BigQuery or Catalog roles found" />
        )}
      </div>

      {/* Section 4: Principal inventory */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Principal Inventory</h3>
        <p className="text-xs text-zinc-500 mb-4">Users and service accounts with dataset-level grants</p>

        {principals.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-zinc-500 border-b border-zinc-800/50">
                  <th className="text-left py-2.5 px-3 font-medium">Principal</th>
                  <th className="text-left py-2.5 px-3 font-medium">Kind</th>
                  <th className="text-right py-2.5 px-3 font-medium">Datasets</th>
                  <th className="text-left py-2.5 px-3 font-medium">Roles</th>
                  <th className="text-left py-2.5 px-3 font-medium">Write</th>
                </tr>
              </thead>
              <tbody>
                {principals.map((p, i) => (
                  <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                    <td className="py-3 px-3 text-white font-mono max-w-[260px] truncate" title={p.principal}>{p.principal}</td>
                    <td className="py-3 px-3">
                      <KindChip kind={p.kind} />
                    </td>
                    <td className="py-3 px-3 text-right text-zinc-300" title={p.datasets.join(', ')}>{p.datasets.length}</td>
                    <td className="py-3 px-3 text-zinc-400 font-mono max-w-[200px] truncate" title={p.roles.join(', ')}>
                      {p.roles.join(', ')}
                    </td>
                    <td className="py-3 px-3">
                      {p.write_capable && (
                        <span className="text-[10px] px-2 py-0.5 rounded-full font-medium border bg-rose-500/10 text-rose-400 border-rose-500/20">
                          WRITE
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState text="No dataset-level principals found" />
        )}
      </div>

      {/* Section 5: Granted but never used */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Granted but Never Used</h3>
        <p className="text-xs text-zinc-500 mb-4">
          Users & service accounts with grants but zero jobs in the selected window — least-privilege cleanup candidates. Group grants excluded (membership not expandable).
        </p>

        {unusedGrants.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-zinc-500 border-b border-zinc-800/50">
                  <th className="text-left py-2.5 px-3 font-medium">Principal</th>
                  <th className="text-left py-2.5 px-3 font-medium">Kind</th>
                  <th className="text-right py-2.5 px-3 font-medium">Datasets</th>
                  <th className="text-left py-2.5 px-3 font-medium">Roles</th>
                  <th className="text-left py-2.5 px-3 font-medium">Write</th>
                </tr>
              </thead>
              <tbody>
                {unusedGrants.map((p, i) => (
                  <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                    <td className="py-3 px-3 text-white font-mono max-w-[260px] truncate" title={p.principal}>{p.principal}</td>
                    <td className="py-3 px-3">
                      <KindChip kind={p.kind} />
                    </td>
                    <td className="py-3 px-3 text-right text-zinc-300" title={p.datasets.join(', ')}>{p.datasets.length}</td>
                    <td className="py-3 px-3 text-zinc-400 font-mono max-w-[200px] truncate" title={p.roles.join(', ')}>
                      {p.roles.join(', ')}
                    </td>
                    <td className="py-3 px-3">
                      {p.write_capable && (
                        <span className="text-[10px] px-2 py-0.5 rounded-full font-medium border bg-rose-500/10 text-rose-400 border-rose-500/20">
                          WRITE
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState text="All principals have recent activity" />
        )}
      </div>

      {/* Section 6: Dataset protection */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Dataset Protection</h3>
        <p className="text-xs text-zinc-500 mb-4">CMEK encryption and default table expiration</p>

        <div className="grid grid-cols-1 md:grid-cols-3 gap-4 mb-6">
          <MetricCard
            label="% CMEK Datasets"
            value={`${cmekPct}%`}
            icon={<Lock size={18} />}
            detail={`${cmekDatasets} of ${datasetPosture.length} encrypted`}
            accentColor="#4ade80"
          />
          <MetricCard
            label="No Default Expiration"
            value={noExpiration.toString()}
            icon={<AlertTriangle size={18} />}
            detail="Tables persist indefinitely"
            accentColor="#fbbf24"
          />
          <MetricCard
            label="Datasets Scanned"
            value={data.datasets_scanned.toString()}
            icon={<Database size={18} />}
            detail={`of ${data.datasets_total} total`}
            accentColor="#38bdf8"
          />
        </div>

        {datasetPosture.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-zinc-500 border-b border-zinc-800/50">
                  <th className="text-left py-2.5 px-3 font-medium">Dataset</th>
                  <th className="text-left py-2.5 px-3 font-medium">CMEK</th>
                  <th className="text-right py-2.5 px-3 font-medium">Default Expiration</th>
                </tr>
              </thead>
              <tbody>
                {datasetPosture.map((d, i) => (
                  <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                    <td className="py-3 px-3 text-white font-mono">{d.dataset}</td>
                    <td className="py-3 px-3">
                      <span
                        className={`text-[10px] px-2 py-0.5 rounded-full font-medium border ${
                          d.cmek
                            ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
                            : 'bg-zinc-500/10 text-zinc-400 border-zinc-500/20'
                        }`}
                        title={d.kms_key || undefined}
                      >
                        {d.cmek ? 'YES' : 'NO'}
                      </span>
                    </td>
                    <td className="py-3 px-3 text-right font-mono">
                      {d.default_exp_days > 0 ? (
                        <span className="text-zinc-300">{d.default_exp_days}d</span>
                      ) : (
                        <span className="text-amber-400">none</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState text="No dataset posture data" />
        )}
      </div>

      {/* Section 7: Column security */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Column Security</h3>
        <p className="text-xs text-zinc-500 mb-4">Policy tags on sensitive columns</p>

        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          <div>
            <MetricCard
              label="Untagged Sensitive Columns"
              value={untaggedSensitive.length.toString()}
              icon={<Tag size={18} />}
              detail="Columns needing policy tags"
              accentColor={untaggedSensitive.length > 0 ? '#fb7185' : '#4ade80'}
            />

            {sensitiveColumns.length > 0 ? (
              <div className="mt-4 overflow-x-auto">
                <table className="w-full text-xs">
                  <thead>
                    <tr className="text-zinc-500 border-b border-zinc-800/50">
                      <th className="text-left py-2.5 px-3 font-medium">Table</th>
                      <th className="text-left py-2.5 px-3 font-medium">Column</th>
                      <th className="text-left py-2.5 px-3 font-medium">Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {sensitiveColumns.map((c, i) => (
                      <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                        <td className="py-3 px-3 text-white font-mono max-w-[200px] truncate" title={`${c.dataset}.${c.table}`}>
                          {c.dataset}.{c.table}
                        </td>
                        <td className="py-3 px-3 text-zinc-300 font-mono">{c.column}</td>
                        <td className="py-3 px-3">
                          <span
                            className={`text-[10px] px-2 py-0.5 rounded-full font-medium border ${
                              c.tagged
                                ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
                                : 'bg-rose-500/10 text-rose-400 border-rose-500/20'
                            }`}
                          >
                            {c.tagged ? 'TAGGED' : 'UNTAGGED'}
                          </span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <div className="mt-4">
                <EmptyState text="No sensitive columns detected" />
              </div>
            )}
          </div>

          <div>
            <h4 className="text-sm font-semibold text-white mb-3">Who Can Bypass Policy Tags</h4>
            <p className="text-xs text-zinc-500 mb-3">Fine-grained readers (datacatalog.categoryFineGrainedGet)</p>
            {tagBypassers.length > 0 ? (
              <div className="space-y-2">
                {tagBypassers.map((bypasser, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <MemberChip member={bypasser} />
                  </div>
                ))}
              </div>
            ) : (
              <p className="text-xs text-zinc-500 italic">No project-level fine-grained readers</p>
            )}
          </div>
        </div>
      </div>

      {/* Section 8: Row-level security */}
      <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
        <h3 className="text-sm font-semibold text-white mb-1">Row-Level Security</h3>
        <p className="text-xs text-zinc-500 mb-4">Active row access policies</p>

        {rlsPolicies.length > 0 ? (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-zinc-500 border-b border-zinc-800/50">
                  <th className="text-left py-2.5 px-3 font-medium">Table</th>
                  <th className="text-left py-2.5 px-3 font-medium">Policy</th>
                  <th className="text-left py-2.5 px-3 font-medium">Predicate</th>
                  <th className="text-right py-2.5 px-3 font-medium">Modified</th>
                </tr>
              </thead>
              <tbody>
                {rlsPolicies.map((rls, i) => (
                  <tr key={i} className="border-b border-zinc-800/30 hover:bg-zinc-800/20 transition-colors">
                    <td className="py-3 px-3 text-white font-mono max-w-[200px] truncate" title={`${rls.dataset}.${rls.table}`}>
                      {rls.dataset}.{rls.table}
                    </td>
                    <td className="py-3 px-3 text-zinc-300 font-mono">{rls.policy}</td>
                    <td className="py-3 px-3 text-zinc-400 font-mono max-w-[300px] truncate" title={rls.predicate}>
                      {rls.predicate}
                    </td>
                    <td className="py-3 px-3 text-right text-zinc-500 font-mono">
                      {formatRelativeTime(rls.modified)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState text="No row access policies defined" />
        )}
      </div>

      {/* Section 9: Visibility limits */}
      <div
        className="rounded-2xl border border-zinc-800/50 p-6"
        style={{ background: '#111114' }}
      >
        <div className="flex items-start gap-3">
          <EyeOff size={16} className="text-zinc-600 shrink-0 mt-0.5" />
          <div>
            <h3 className="text-sm font-semibold text-white mb-2">Visibility Limits</h3>
            <p className="text-xs text-zinc-500 mb-3">What this posture scan does NOT cover:</p>
            <ul className="text-xs text-zinc-500 space-y-1.5">
              <li className="flex items-start gap-2">
                <span className="text-zinc-700 shrink-0">•</span>
                <span>Inherited folder/org IAM (requires higher-level access)</span>
              </li>
              <li className="flex items-start gap-2">
                <span className="text-zinc-700 shrink-0">•</span>
                <span>Group membership expansion (group: grants listed as-is)</span>
              </li>
              <li className="flex items-start gap-2">
                <span className="text-zinc-700 shrink-0">•</span>
                <span>IAM change history (requires audit logs)</span>
              </li>
              <li className="flex items-start gap-2">
                <span className="text-zinc-700 shrink-0">•</span>
                <span>Denied-access attempts (not logged in INFORMATION_SCHEMA)</span>
              </li>
              <li className="flex items-start gap-2">
                <span className="text-zinc-700 shrink-0">•</span>
                <span>Storage Read API / tabledata.list reads (JOBS only captures query/load/extract/copy)</span>
              </li>
              <li className="flex items-start gap-2">
                <span className="text-zinc-700 shrink-0">•</span>
                <span>History beyond 180 days (INFORMATION_SCHEMA retention)</span>
              </li>
            </ul>
          </div>
        </div>
      </div>
    </div>
  );
}

// --- Helper Components ---

function KindChip({ kind }: { kind: string }) {
  const config: Record<string, { label: string; color: string; icon: React.ReactNode }> = {
    user: { label: 'USER', color: 'cyan', icon: <Users size={9} /> },
    serviceAccount: { label: 'SERVICE ACCT', color: 'purple', icon: <Bot size={9} /> },
    group: { label: 'GROUP', color: 'zinc', icon: <Building2 size={9} /> },
    domain: { label: 'DOMAIN', color: 'amber', icon: <Building2 size={9} /> },
    special: { label: 'SPECIAL', color: 'zinc', icon: <Key size={9} /> },
    public: { label: 'PUBLIC', color: 'rose', icon: <AlertTriangle size={9} /> },
  };

  const { label, color, icon } = config[kind] || config.special;

  return (
    <span
      className={`inline-flex items-center gap-1 text-[10px] px-2 py-0.5 rounded-full font-medium border bg-${color}-500/10 text-${color}-400 border-${color}-500/20`}
      style={{
        background: `rgba(${
          color === 'cyan' ? '56,189,248' :
          color === 'purple' ? '192,132,252' :
          color === 'amber' ? '251,191,36' :
          color === 'rose' ? '251,113,133' :
          '161,161,170'
        },0.1)`,
        color: color === 'cyan' ? '#38bdf8' :
               color === 'purple' ? '#c084fc' :
               color === 'amber' ? '#fbbf24' :
               color === 'rose' ? '#fb7185' :
               '#a1a1aa',
        borderColor: `rgba(${
          color === 'cyan' ? '56,189,248' :
          color === 'purple' ? '192,132,252' :
          color === 'amber' ? '251,191,36' :
          color === 'rose' ? '251,113,133' :
          '161,161,170'
        },0.2)`,
      }}
    >
      {icon}
      {label}
    </span>
  );
}

function MemberChip({ member }: { member: string }) {
  let icon: React.ReactNode;
  let color: string;

  if (member.startsWith('user:')) {
    icon = <Users size={10} />;
    color = 'cyan';
  } else if (member.startsWith('serviceAccount:')) {
    icon = <Bot size={10} />;
    color = 'purple';
  } else {
    icon = <Building2 size={10} />;
    color = 'zinc';
  }

  return (
    <span
      className={`inline-flex items-center gap-1.5 text-[10px] px-2 py-1 rounded-full font-mono border`}
      style={{
        background: `rgba(${
          color === 'cyan' ? '56,189,248' :
          color === 'purple' ? '192,132,252' :
          '161,161,170'
        },0.1)`,
        color: color === 'cyan' ? '#38bdf8' :
               color === 'purple' ? '#c084fc' :
               '#a1a1aa',
        borderColor: `rgba(${
          color === 'cyan' ? '56,189,248' :
          color === 'purple' ? '192,132,252' :
          '161,161,170'
        },0.2)`,
      }}
    >
      {icon}
      <span className="max-w-[180px] truncate">{member}</span>
    </span>
  );
}

function formatRelativeTime(iso: string): string {
  if (!iso) return 'N/A';
  const diff = Date.now() - new Date(iso).getTime();
  const days = Math.floor(diff / 86400000);
  if (days === 0) return 'Today';
  if (days === 1) return 'Yesterday';
  if (days < 7) return `${days}d ago`;
  if (days < 30) return `${Math.floor(days / 7)}w ago`;
  return `${Math.floor(days / 30)}mo ago`;
}

function LoadingPulse() {
  return (
    <div className="space-y-4">
      {[1, 2, 3, 4, 5, 6].map(i => (
        <div key={i} className="h-64 rounded-2xl animate-pulse" style={{ background: '#111114' }} />
      ))}
    </div>
  );
}

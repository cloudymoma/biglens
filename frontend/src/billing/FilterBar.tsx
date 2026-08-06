import type { BillingFilterState, BillingMeta } from '../types';

export interface FilterBarProps {
  filter: BillingFilterState;
  meta: BillingMeta;
  onChange: (f: BillingFilterState) => void;
}

function isoDaysAgo(days: number): string {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() - days);
  return d.toISOString().slice(0, 10);
}

function monthToDateStart(): string {
  return `${new Date().toISOString().slice(0, 8)}01`;
}

// Presets set [start, end); "Last month" spans the previous calendar month.
const PRESETS = [
  { id: '7d', label: '7d' },
  { id: '30d', label: '30d' },
  { id: 'mtd', label: 'MTD' },
  { id: 'lastmonth', label: 'Last month' },
] as const;

function applyPreset(f: BillingFilterState, id: string): BillingFilterState {
  const today = isoDaysAgo(0);
  if (id === '7d') return { ...f, invoiceMonth: '', start: isoDaysAgo(7), end: today };
  if (id === '30d') return { ...f, invoiceMonth: '', start: isoDaysAgo(30), end: today };
  if (id === 'mtd') return { ...f, invoiceMonth: '', start: monthToDateStart(), end: today };
  // lastmonth
  const d = new Date();
  d.setUTCDate(1);
  const end = d.toISOString().slice(0, 10);
  d.setUTCMonth(d.getUTCMonth() - 1);
  const start = d.toISOString().slice(0, 10);
  return { ...f, invoiceMonth: '', start, end };
}

const selectCls = 'bg-zinc-900 border border-zinc-700 rounded-lg px-2 py-1.5 text-xs text-zinc-200';

export default function FilterBar({ filter, meta, onChange }: FilterBarProps) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <div className="flex items-center gap-1">
        {PRESETS.map(p => (
          <button
            key={p.id}
            onClick={() => onChange(applyPreset(filter, p.id))}
            className="px-2 py-1 rounded-lg text-[11px] text-zinc-400 hover:text-zinc-200 bg-zinc-900 border border-zinc-800"
          >
            {p.label}
          </button>
        ))}
      </div>
      <input
        type="date"
        value={filter.start}
        onChange={e => onChange({ ...filter, invoiceMonth: '', start: e.target.value })}
        disabled={!!filter.invoiceMonth}
        className={selectCls}
      />
      <span className="text-zinc-600 text-xs">→</span>
      <input
        type="date"
        value={filter.end}
        onChange={e => onChange({ ...filter, invoiceMonth: '', end: e.target.value })}
        disabled={!!filter.invoiceMonth}
        className={selectCls}
      />
      <select
        value={filter.invoiceMonth}
        onChange={e => onChange({ ...filter, invoiceMonth: e.target.value })}
        className={selectCls}
        title="Invoice month mode reconciles with invoices (includes tax and adjustments)"
      >
        <option value="">Usage dates</option>
        {meta.invoice_months.map(m => (
          <option key={m} value={m}>Invoice {m}</option>
        ))}
      </select>
      {meta.dataset.billing_accounts.length > 1 && (
        <select
          value={filter.accounts[0] ?? ''}
          onChange={e => onChange({ ...filter, accounts: e.target.value ? [e.target.value] : [] })}
          className={selectCls}
        >
          <option value="">All accounts</option>
          {meta.dataset.billing_accounts.map(a => (
            <option key={a} value={a}>{a}</option>
          ))}
        </select>
      )}
      <select
        value={filter.projects[0] ?? ''}
        onChange={e => onChange({ ...filter, projects: e.target.value ? [e.target.value] : [] })}
        className={selectCls}
      >
        <option value="">All projects</option>
        {meta.projects.map(p => (
          <option key={p.id} value={p.id}>{p.name || p.id}</option>
        ))}
      </select>
      <select
        value={filter.services[0] ?? ''}
        onChange={e => onChange({ ...filter, services: e.target.value ? [e.target.value] : [] })}
        className={selectCls}
      >
        <option value="">All services</option>
        {meta.services.map(s => (
          <option key={s} value={s}>{s}</option>
        ))}
      </select>
      <select
        value={filter.labelKey}
        onChange={e => onChange({ ...filter, labelKey: e.target.value, labelValue: '' })}
        className={selectCls}
      >
        <option value="">No label filter</option>
        {meta.label_keys.map(k => (
          <option key={k} value={k}>{k}</option>
        ))}
      </select>
      {filter.labelKey && (
        <input
          value={filter.labelValue}
          onChange={e => onChange({ ...filter, labelValue: e.target.value })}
          placeholder="label value"
          className={selectCls}
        />
      )}
    </div>
  );
}

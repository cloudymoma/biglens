import { useEffect, useState } from 'react';
import type { BillingFilterState, BillingMeta } from '../types';

export interface FilterBarProps {
  filter: BillingFilterState;
  meta: BillingMeta;
  onChange: (f: BillingFilterState) => void;
}

function pacificParts(): { y: number; m: number; d: number } {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: 'America/Los_Angeles',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(new Date());
  return {
    y: Number(parts.find(p => p.type === 'year')?.value ?? 1970),
    m: Number(parts.find(p => p.type === 'month')?.value ?? 1),
    d: Number(parts.find(p => p.type === 'day')?.value ?? 1),
  };
}

function pacificDateOffset(days: number): string {
  const { y, m, d } = pacificParts();
  return new Date(Date.UTC(y, m - 1, d + days)).toISOString().slice(0, 10);
}

function monthToDateStart(): string {
  const { y, m } = pacificParts();
  return new Date(Date.UTC(y, m - 1, 1)).toISOString().slice(0, 10);
}

// Presets set [start, end); "Last month" spans the previous calendar month in PT.
const PRESETS = [
  { id: '7d', label: '7d' },
  { id: '30d', label: '30d' },
  { id: 'mtd', label: 'MTD' },
  { id: 'lastmonth', label: 'Last month' },
] as const;

function applyPreset(f: BillingFilterState, id: string): BillingFilterState {
  const today = pacificDateOffset(0);
  if (id === '7d') return { ...f, invoiceMonth: '', start: pacificDateOffset(-7), end: today };
  if (id === '30d') return { ...f, invoiceMonth: '', start: pacificDateOffset(-30), end: today };
  if (id === 'mtd') {
    const start = monthToDateStart();
    const end = start === today ? pacificDateOffset(1) : today;
    return { ...f, invoiceMonth: '', start, end };
  }
  // lastmonth in Pacific Time
  const { y, m } = pacificParts();
  const end = new Date(Date.UTC(y, m - 1, 1)).toISOString().slice(0, 10);
  const start = new Date(Date.UTC(y, m - 2, 1)).toISOString().slice(0, 10);
  return { ...f, invoiceMonth: '', start, end };
}

function isValidBillingDate(s: string): boolean {
  return /^\d{4}-\d{2}-\d{2}$/.test(s) && s >= '2017-01-01';
}

const selectCls = 'bg-zinc-900 border border-zinc-700 rounded-lg px-2 py-1.5 text-xs text-zinc-200';

export default function FilterBar({ filter, meta, onChange }: FilterBarProps) {
  const [labelDraft, setLabelDraft] = useState(filter.labelValue);

  useEffect(() => {
    setLabelDraft(filter.labelValue);
  }, [filter.labelKey, filter.labelValue]);

  const commitLabel = () => {
    const trimmed = labelDraft.trim();
    if (trimmed !== filter.labelValue) {
      onChange({ ...filter, labelValue: trimmed });
    }
  };

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
        min="2017-01-01"
        value={filter.start}
        onChange={e => {
          const v = e.target.value;
          if (isValidBillingDate(v) && v < filter.end) {
            onChange({ ...filter, invoiceMonth: '', start: v });
          }
        }}
        disabled={!!filter.invoiceMonth}
        className={selectCls}
      />
      <span className="text-zinc-600 text-xs">→</span>
      <input
        type="date"
        min="2017-01-02"
        value={filter.end}
        onChange={e => {
          const v = e.target.value;
          if (isValidBillingDate(v) && v > filter.start) {
            onChange({ ...filter, invoiceMonth: '', end: v });
          }
        }}
        disabled={!!filter.invoiceMonth}
        className={selectCls}
      />
      <select
        value={filter.invoiceMonth}
        onChange={e => onChange({ ...filter, invoiceMonth: e.target.value })}
        className={selectCls}
        title="Invoice month mode reconciles with invoices (includes tax and adjustments)"
      >
        <option value="">Usage dates (PT)</option>
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
          value={labelDraft}
          onChange={e => setLabelDraft(e.target.value)}
          onBlur={commitLabel}
          onKeyDown={e => {
            if (e.key === 'Enter') commitLabel();
          }}
          placeholder="label value (Enter to apply)"
          className={selectCls}
        />
      )}
    </div>
  );
}

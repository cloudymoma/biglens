import type React from 'react';

export function Card({ title, subtitle, action, children }: {
  title: string; subtitle?: string; action?: React.ReactNode; children: React.ReactNode;
}) {
  return (
    <div className="rounded-2xl border border-zinc-800/50 p-6" style={{ background: '#111114' }}>
      <div className="flex items-start justify-between gap-3 mb-4">
        <div>
          <h3 className="text-sm font-semibold text-white">{title}</h3>
          {subtitle && <p className="text-xs text-zinc-500 mt-0.5">{subtitle}</p>}
        </div>
        {action}
      </div>
      {children}
    </div>
  );
}

export function GroupLabel({ children }: { children: React.ReactNode }) {
  return (
    <p className="text-[10px] font-semibold text-zinc-600 uppercase tracking-[0.15em] mb-2">{children}</p>
  );
}

const INPUT_CLASS = 'w-full text-xs text-zinc-200 font-mono rounded-lg px-3 py-2 outline-none border border-zinc-800/50 transition-colors focus:border-cyan-500/30 disabled:opacity-30';

export function NumField({ label, value, onChange, unit, step, min = 0, disabled }: {
  label: string; value: number; onChange: (v: number) => void;
  unit?: string; step?: number; min?: number; disabled?: boolean;
}) {
  return (
    <div>
      <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">{label}</label>
      <div className="relative">
        <input
          type="number"
          value={Number.isFinite(value) ? value : ''}
          min={min}
          step={step ?? 'any'}
          disabled={disabled}
          onChange={e => onChange(e.target.value === '' ? 0 : Number(e.target.value))}
          className={`${INPUT_CLASS} ${unit ? 'pr-16' : ''}`}
          style={{ background: '#09090b' }}
        />
        {unit && (
          <span className="absolute right-3 top-1/2 -translate-y-1/2 text-[10px] text-zinc-600 pointer-events-none">{unit}</span>
        )}
      </div>
    </div>
  );
}

export function SelectField({ label, value, options, onChange, disabled }: {
  label: string; value: string; options: { value: string; label: string }[];
  onChange: (v: string) => void; disabled?: boolean;
}) {
  return (
    <div>
      <label className="text-[10px] font-mono text-zinc-600 uppercase block mb-1 px-0.5">{label}</label>
      <select
        value={value}
        disabled={disabled}
        onChange={e => onChange(e.target.value)}
        className={`${INPUT_CLASS} cursor-pointer`}
        style={{ background: '#09090b' }}
      >
        {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
      </select>
    </div>
  );
}

// Backend notices for a calculator tab: request errors, edition/rounding
// warnings, and a quiet "updating" hint while a recalculation is in flight.
export function CalcStatus({ error, warnings, loading }: { error: string | null; warnings?: string[]; loading: boolean }) {
  return (
    <div className="space-y-2 empty:hidden">
      {error && (
        <div className="rounded-xl border border-rose-500/30 bg-rose-500/5 px-4 py-2.5 text-xs text-rose-300">
          Could not calculate: {error}
        </div>
      )}
      {warnings && warnings.length > 0 && (
        <ul className="rounded-xl border border-amber-500/20 bg-amber-500/5 px-4 py-2.5 text-xs text-amber-200/90 space-y-1">
          {warnings.map(w => <li key={w}>{w}</li>)}
        </ul>
      )}
      {loading && !error && (
        <p className="text-[10px] text-zinc-600 px-1 animate-pulse">Updating estimate…</p>
      )}
    </div>
  );
}

export function ResetButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className="text-[10px] font-medium text-cyan-400/80 hover:text-cyan-300 cursor-pointer whitespace-nowrap"
    >
      Reset to list
    </button>
  );
}

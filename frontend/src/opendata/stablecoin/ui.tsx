import { useRef } from 'react';
import type { KeyboardEvent, ReactNode } from 'react';
import { Download, ExternalLink } from 'lucide-react';
import HelpTip from '../crypto/HelpTip';
import SourceBadge from '../common/SourceBadge';
import { downloadCsv } from '../common/format';
import type { SourceStatus } from '../common/types';
import { rovingIndex } from './a11y';
import { safeHref } from './deeplink';
import { fmtTimeHM } from './format';
import { cx } from './styles';
import { useStableTheme } from './theme';

// Shared building blocks of the stablecoin dashboard. Every panel has a
// title, an optional help tip with its formula, source/as-of information and
// a CSV export where it shows data (§7.3).

export function Panel({
  title,
  help,
  right,
  children,
  className = '',
}: {
  title: string;
  help?: ReactNode;
  right?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`${cx.card} ${className}`} aria-label={title}>
      <div className="mb-3 flex flex-wrap items-start justify-between gap-2">
        <div className="flex items-center gap-1.5">
          <h3 className={cx.title}>{title}</h3>
          {help && <HelpTip label={`How "${title}" is computed`}>{help}</HelpTip>}
        </div>
        {right && <div className="flex flex-wrap items-center gap-2">{right}</div>}
      </div>
      {children}
    </section>
  );
}

export function AsOf({ iso, label = 'as of' }: { iso?: string | null; label?: string }) {
  if (!iso) return null;
  return (
    <span className={`text-[11px] ${cx.muted}`} title={iso}>
      {label} {fmtTimeHM(iso)}
    </span>
  );
}

type CsvCell = string | number | null | undefined;

export function CsvButton({
  filename,
  label,
  headers,
  rows,
}: {
  filename: string;
  label: string; // what is exported, for the accessible name
  headers: string[];
  rows: () => CsvCell[][];
}) {
  return (
    <button
      type="button"
      onClick={() => downloadCsv(filename, headers, rows())}
      aria-label={`Download ${label} as CSV`}
      title={`Download ${label} as CSV`}
      className={`${cx.buttonSmall} ${cx.focus}`}
    >
      <Download className="h-3.5 w-3.5" aria-hidden="true" />
      CSV
    </button>
  );
}

// ColdState is what a panel shows before its data exists (§7.4).
export function ColdState({ children }: { children: ReactNode }) {
  return (
    <div className={`flex min-h-[96px] items-center justify-center rounded-xl border border-dashed px-4 py-6 text-center text-xs ${cx.border} ${cx.muted}`}>
      {children}
    </div>
  );
}

export function ErrorNote({ message }: { message: string }) {
  if (!message) return null;
  return (
    <div
      role="alert"
      className="rounded-xl border border-red-300 bg-red-50 px-3 py-2 text-xs text-red-800 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300"
    >
      Could not load this view: {message}
    </div>
  );
}

export function Stat({ label, value, sub, title }: { label: string; value: ReactNode; sub?: ReactNode; title?: string }) {
  return (
    <div className={cx.inset} title={title}>
      <div className={`text-[11px] ${cx.muted}`}>{label}</div>
      <div className={`mt-0.5 font-mono text-base font-semibold ${cx.strong}`}>{value}</div>
      {sub && <div className={`mt-0.5 text-[11px] ${cx.muted}`}>{sub}</div>}
    </div>
  );
}

// ExtLink renders absolute http(s) URLs only (see safeHref); anything else
// is shown as text.
export function ExtLink({ href, children, className = '' }: { href?: string | null; children: ReactNode; className?: string }) {
  const safe = safeHref(href);
  if (!safe) return <span className={className}>{children}</span>;
  return (
    <a href={safe} target="_blank" rel="noopener noreferrer" className={`${cx.link} ${cx.focus} inline-flex items-center gap-0.5 ${className}`}>
      {children}
      <ExternalLink className="h-3 w-3 shrink-0" aria-hidden="true" />
      <span className="sr-only">(opens in a new tab)</span>
    </a>
  );
}

export function Sources({ sources }: { sources: SourceStatus[] }) {
  const theme = useStableTheme();
  if (sources.length === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {sources.map(s => (
        <SourceBadge key={`${s.id}-${s.via ?? ''}`} source={s} theme={theme} />
      ))}
    </div>
  );
}

export interface Choice<T extends string> {
  id: T;
  label: string;
}

// SegmentedControl is a radio group: one tab stop, arrow keys move the
// selection (WAI-ARIA radio group pattern).
export function SegmentedControl<T extends string>({
  label,
  options,
  value,
  onChange,
}: {
  label: string;
  options: readonly Choice<T>[];
  value: T;
  onChange: (v: T) => void;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const onKey = (e: KeyboardEvent<HTMLButtonElement>, i: number) => {
    const next = rovingIndex(e.key, i, options.length);
    if (next === null) return;
    e.preventDefault();
    onChange(options[next].id);
    refs.current[next]?.focus();
  };
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="inline-flex rounded-lg border border-zinc-200 bg-white p-0.5 text-xs dark:border-zinc-800 dark:bg-zinc-900/80"
    >
      {options.map((o, i) => {
        const on = o.id === value;
        return (
          <button
            key={o.id}
            ref={el => {
              refs.current[i] = el;
            }}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            onClick={() => onChange(o.id)}
            onKeyDown={e => onKey(e, i)}
            className={`rounded-md px-2.5 py-1 font-medium transition ${cx.focus} ${on ? cx.segOn : cx.segOff}`}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}

// TabBar is a WAI-ARIA tablist with roving focus; panels use
// tabPanelProps for the matching ids.
export function TabBar<T extends string>({
  label,
  tabs,
  active,
  onSelect,
  idPrefix,
}: {
  label: string;
  tabs: readonly Choice<T>[];
  active: T;
  onSelect: (id: T) => void;
  idPrefix: string;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const onKey = (e: KeyboardEvent<HTMLButtonElement>, i: number) => {
    const next = rovingIndex(e.key, i, tabs.length);
    if (next === null) return;
    e.preventDefault();
    onSelect(tabs[next].id);
    refs.current[next]?.focus();
  };
  return (
    <div role="tablist" aria-label={label} className={`flex flex-wrap items-center gap-1.5 border-b pb-2 ${cx.border}`}>
      {tabs.map((t, i) => {
        const on = t.id === active;
        return (
          <button
            key={t.id}
            ref={el => {
              refs.current[i] = el;
            }}
            type="button"
            role="tab"
            id={`${idPrefix}-tab-${t.id}`}
            aria-selected={on}
            aria-controls={`${idPrefix}-panel-${t.id}`}
            tabIndex={on ? 0 : -1}
            onClick={() => onSelect(t.id)}
            onKeyDown={e => onKey(e, i)}
            className={`rounded-lg px-3.5 py-1.5 text-xs font-semibold transition ${cx.focus} ${
              on
                ? 'bg-emerald-600/10 text-emerald-800 ring-1 ring-emerald-600/30 dark:bg-emerald-500/15 dark:text-emerald-300 dark:ring-emerald-500/30'
                : 'text-zinc-600 hover:bg-zinc-100 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-800/60 dark:hover:text-zinc-200'
            }`}
          >
            {t.label}
          </button>
        );
      })}
    </div>
  );
}

export function KV({ k, children }: { k: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3 py-0.5 text-xs">
      <span className={cx.muted}>{k}</span>
      <span className={`text-right ${cx.text}`}>{children}</span>
    </div>
  );
}

// Shared class strings for the stablecoin dashboard: light classes with
// `dark:` variants (the root toggles the `dark` class, see theme.ts). Each
// string avoids utilities that a caller would need to override (Tailwind
// resolves conflicting utilities by stylesheet order, not class order), so
// variants exist instead: td / tdWrap, card / cardTight, button / buttonSmall.
const cardBase =
  'rounded-2xl border border-zinc-200 bg-white shadow-sm dark:border-zinc-800/60 dark:bg-[#111114] dark:shadow-none';
const tdBase = 'px-2 py-1.5 text-xs text-zinc-700 dark:text-zinc-300';
const buttonBase =
  'inline-flex items-center gap-1.5 rounded-lg border border-zinc-200 bg-white text-zinc-700 transition hover:border-zinc-300 hover:text-zinc-900 dark:border-zinc-800 dark:bg-zinc-900/80 dark:text-zinc-300 dark:hover:border-zinc-700 dark:hover:text-zinc-100';

export const cx = {
  card: `${cardBase} p-4`,
  cardTight: `${cardBase} p-3`,
  inset: 'rounded-xl border border-zinc-200 bg-zinc-50 p-3 dark:border-zinc-800/70 dark:bg-zinc-900/50',
  title: 'text-sm font-semibold text-zinc-900 dark:text-zinc-100',
  strong: 'text-zinc-900 dark:text-zinc-100',
  text: 'text-zinc-700 dark:text-zinc-300',
  muted: 'text-zinc-500 dark:text-zinc-400',
  faint: 'text-zinc-400 dark:text-zinc-500',
  border: 'border-zinc-200 dark:border-zinc-800',
  link: 'text-sky-700 underline-offset-2 hover:underline dark:text-sky-400',
  table: 'w-full border-collapse text-left',
  th: 'whitespace-nowrap px-2 py-1.5 text-[11px] font-medium text-zinc-500 dark:text-zinc-400',
  td: `whitespace-nowrap ${tdBase}`,
  tdWrap: `whitespace-normal ${tdBase}`,
  tr: 'border-t border-zinc-100 dark:border-zinc-800/70',
  mono: 'font-mono',
  focus: 'focus:outline-none focus-visible:ring-2 focus-visible:ring-sky-500 dark:focus-visible:ring-sky-400',
  button: `${buttonBase} px-2.5 py-1.5 text-xs`,
  buttonSmall: `${buttonBase} px-2 py-1 text-[11px]`,
} as const;

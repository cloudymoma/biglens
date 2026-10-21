import { CircleCheck, CircleHelp, Eye, Info, Minus, OctagonAlert, TriangleAlert } from 'lucide-react';
import type { LucideIcon } from 'lucide-react';
import { stateMeta } from './signals';
import type { StateIcon } from './signals';
import { useStableTheme } from './theme';

const ICONS: Record<StateIcon, LucideIcon> = { CircleCheck, Info, Eye, TriangleAlert, OctagonAlert, CircleHelp, Minus };

// SignalChip shows a signal state as icon + word (never color alone, §7.3).
// The word keeps the theme's text color for contrast; the icon and border
// carry the state color. An optional value (e.g. a deviation) precedes the
// word.
export default function SignalChip({
  state,
  prefix,
  value,
  title,
}: {
  state: string | null | undefined;
  prefix?: string; // e.g. the signal id ("S1") or a short label
  value?: string;
  title?: string;
}) {
  const theme = useStableTheme();
  const m = stateMeta(state);
  const Icon = ICONS[m.icon];
  const color = m.color[theme];
  return (
    <span
      title={title}
      className="inline-flex items-center gap-1 whitespace-nowrap rounded-full border px-2 py-0.5 text-[11px] font-medium leading-4 text-zinc-800 dark:text-zinc-100"
      style={{ borderColor: color, backgroundColor: `${color}1f` }}
    >
      <Icon aria-hidden="true" className="h-3.5 w-3.5 shrink-0" style={{ color }} strokeWidth={2.25} />
      {prefix && <span className="text-zinc-500 dark:text-zinc-400">{prefix}</span>}
      {value && <span className="font-mono">{value} ·</span>}
      <span>{m.label}</span>
    </span>
  );
}

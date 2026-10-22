// Signal states (stablecoin.md §6.1, §7.3): every chip shows the state word
// and an icon, so a state is never conveyed by color alone. Colors come from
// the Okabe–Ito / Tol color-blind-safe families, one variant per theme, and
// tests check their contrast on the panel surface and their distinctness
// under simulated protanopia and deuteranopia. Pure data, no imports: the
// icon is a lucide-react component name resolved by SignalChip.

export type Theme = 'light' | 'dark';

export type SignalState = 'normal' | 'info' | 'watch' | 'warning' | 'critical' | 'unknown' | 'n/a';

export type StateIcon = 'CircleCheck' | 'Info' | 'Eye' | 'TriangleAlert' | 'OctagonAlert' | 'CircleHelp' | 'Minus';

export interface StateMeta {
  state: string;
  label: string; // the word shown on the chip
  icon: StateIcon;
  color: Record<Theme, string>;
  rank: number; // severity order for sorting (n/a lowest)
}

// Panel backgrounds the colors are checked against.
export const THEME_SURFACE: Record<Theme, string> = { light: '#ffffff', dark: '#111114' };

export const SIGNAL_STATES: readonly SignalState[] = ['normal', 'info', 'watch', 'warning', 'critical', 'unknown', 'n/a'];

const META: Record<SignalState, Omit<StateMeta, 'state'>> = {
  normal: { label: 'normal', icon: 'CircleCheck', color: { light: '#007A5A', dark: '#26B386' }, rank: 1 },
  info: { label: 'info', icon: 'Info', color: { light: '#0072B2', dark: '#56B4E9' }, rank: 1 },
  watch: { label: 'watch', icon: 'Eye', color: { light: '#B8860B', dark: '#EAB308' }, rank: 3 },
  warning: { label: 'warning', icon: 'TriangleAlert', color: { light: '#A63A00', dark: '#D9480F' }, rank: 4 },
  critical: { label: 'critical', icon: 'OctagonAlert', color: { light: '#6F1F7A', dark: '#B07CE8' }, rank: 5 },
  unknown: { label: 'unknown', icon: 'CircleHelp', color: { light: '#71717A', dark: '#A1A1AA' }, rank: 2 },
  'n/a': { label: 'n/a', icon: 'Minus', color: { light: '#71717A', dark: '#A1A1AA' }, rank: 0 },
};

// stateMeta never returns a blank chip: an unexpected state keeps its own
// word with the unknown icon; a missing one reads "unknown".
export function stateMeta(state: string | null | undefined): StateMeta {
  const s = state || 'unknown';
  const m = (META as Record<string, Omit<StateMeta, 'state'>>)[s];
  if (m) return { state: s, ...m };
  return { state: s, ...META.unknown, label: s };
}

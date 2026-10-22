// Accessible signal states (stablecoin.md §7.3): a state is never shown by
// color alone — every chip carries the state word and an icon — and the
// palette is checked for color-blind safety (deuteranopia, protanopia) in
// the light and dark themes.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { SIGNAL_STATES, stateMeta, THEME_SURFACE } from '../src/opendata/stablecoin/signals.ts';
import type { Theme } from '../src/opendata/stablecoin/signals.ts';

const THEMES: Theme[] = ['light', 'dark'];

// sRGB → linear → relative luminance / CIE Lab (D65).
const channels = (hex: string): number[] => [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255);
const linear = (c: number): number => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
const luminance = (hex: string): number => {
  const [r, g, b] = channels(hex).map(linear);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
const contrast = (a: string, b: string): number => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
};
// Viénot, Brettel & Mollon (1999) dichromat simulation in linear RGB.
const CVD: Record<string, number[][] | null> = {
  normal: null,
  protanopia: [[0.11238, 0.88762, 0], [0.11238, 0.88762, 0], [0.00401, -0.00401, 1]],
  deuteranopia: [[0.29275, 0.70725, 0], [0.29275, 0.70725, 0], [-0.02234, 0.02234, 1]],
};
const lab = (hex: string, m: number[][] | null): number[] => {
  let c = channels(hex).map(linear);
  if (m) c = m.map(row => Math.min(1, Math.max(0, row[0] * c[0] + row[1] * c[1] + row[2] * c[2])));
  const [r, g, b] = c;
  const x = 0.4124564 * r + 0.3575761 * g + 0.1804375 * b;
  const y = 0.2126729 * r + 0.7151522 * g + 0.072175 * b;
  const z = 0.0193339 * r + 0.119192 * g + 0.9503041 * b;
  const f = (t: number): number => (t > (6 / 29) ** 3 ? Math.cbrt(t) : t / (3 * (6 / 29) ** 2) + 4 / 29);
  const [fx, fy, fz] = [f(x / 0.95047), f(y), f(z / 1.08883)];
  return [116 * fy - 16, 500 * (fx - fy), 200 * (fy - fz)];
};
const deltaE = (a: number[], b: number[]): number => Math.hypot(a[0] - b[0], a[1] - b[1], a[2] - b[2]);

test('every state has a word and its own icon', () => {
  const icons = new Set<string>();
  for (const s of SIGNAL_STATES) {
    const m = stateMeta(s);
    assert.ok(m.label.length > 0, `${s} has no word`);
    assert.ok(!icons.has(m.icon), `${s} reuses icon ${m.icon}`);
    icons.add(m.icon);
  }
  // Unexpected states from the API still render a word, never a blank chip.
  assert.equal(stateMeta('something_new').label, 'something_new');
  assert.equal(stateMeta(undefined).label, 'unknown');
  assert.equal(stateMeta('').state, 'unknown');
  assert.equal(stateMeta('').label, 'unknown');
});

test('state colors are non-text contrast ≥ 3:1 on the panel surface (WCAG 1.4.11)', () => {
  for (const theme of THEMES) {
    for (const s of SIGNAL_STATES) {
      const c = stateMeta(s).color[theme];
      assert.ok(contrast(c, THEME_SURFACE[theme]) >= 3, `${s} ${c} on ${theme} = ${contrast(c, THEME_SURFACE[theme]).toFixed(2)}:1`);
    }
  }
});

test('severity colors stay distinct for protanopia and deuteranopia (ΔE76 ≥ 10)', () => {
  const severities = ['normal', 'watch', 'warning', 'critical'] as const;
  for (const theme of THEMES) {
    for (const [vision, m] of Object.entries(CVD)) {
      for (let i = 0; i < severities.length; i++) {
        for (let j = i + 1; j < severities.length; j++) {
          const a = stateMeta(severities[i]).color[theme];
          const b = stateMeta(severities[j]).color[theme];
          const d = deltaE(lab(a, m), lab(b, m));
          assert.ok(d >= 10, `${theme} ${vision}: ${severities[i]} ${a} vs ${severities[j]} ${b} ΔE ${d.toFixed(1)}`);
        }
      }
    }
  }
});

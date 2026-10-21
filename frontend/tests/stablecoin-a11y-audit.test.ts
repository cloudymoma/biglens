// Accessibility and escaping invariants of the stablecoin dashboard
// (stablecoin.md §7.3, §11, §12.4):
//  - small text meets WCAG AA (4.5:1) on the panel surfaces in both themes,
//    including the selected state of the asset/range selectors;
//  - every chart gets its ARIA description and chrome from baseOption, and
//    every tooltip builds its HTML through tipHtml (which escapes);
//  - no raw HTML injection anywhere in the dashboard.
// Colors come from Tailwind's own theme (OKLCH), so the check follows the
// palette the build uses.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';

const dir = new URL('../src/opendata/stablecoin/', import.meta.url);
const read = (name: string): string => readFileSync(new URL(name, dir), 'utf8');
const theme = readFileSync(new URL('../node_modules/tailwindcss/theme.css', import.meta.url), 'utf8');

// Linear-light luminance of a Tailwind color (oklch(...) or #hex).
function luminance(color: string): number {
  if (color === 'white') return 1;
  const m = theme.match(new RegExp(`--color-${color}:\\s*([^;]+);`));
  const value = m ? m[1].trim() : color;
  let rgb: number[];
  const ok = value.match(/^oklch\(([\d.]+)%\s+([\d.]+)\s+([\d.]+)\)$/);
  if (ok) {
    const [l, c, h] = [Number(ok[1]) / 100, Number(ok[2]), (Number(ok[3]) * Math.PI) / 180];
    const a = c * Math.cos(h);
    const b = c * Math.sin(h);
    const L = (l + 0.3963377774 * a + 0.2158037573 * b) ** 3;
    const M = (l - 0.1055613458 * a - 0.0638541728 * b) ** 3;
    const S = (l - 0.0894841775 * a - 1.291485548 * b) ** 3;
    rgb = [
      4.0767416621 * L - 3.3077115913 * M + 0.2309699292 * S,
      -1.2684380046 * L + 2.6097574011 * M - 0.3413193965 * S,
      -0.0041960863 * L - 0.7034186147 * M + 1.707614701 * S,
    ];
  } else if (/^#[0-9a-f]{6}$/i.test(value)) {
    rgb = [1, 3, 5].map(i => parseInt(value.slice(i, i + 2), 16) / 255).map(x => (x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4));
  } else {
    throw new Error(`unknown color ${color} (${value})`);
  }
  const [r, g, b] = rgb.map(x => Math.min(1, Math.max(0, x)));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

const contrast = (a: string, b: string): number => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
};

// The class string of a cx token in styles.ts, with `${const}` references to
// the file's own string constants resolved.
function token(name: string): string {
  const src = read('styles.ts');
  const m = src.match(new RegExp(`\\b${name}: (?:'([^']*)'|\`([^\`]*)\`)`));
  assert.ok(m, `styles.ts has no token ${name}`);
  return (m[1] ?? m[2]).replace(/\$\{(\w+)\}/g, (_, ref: string) => {
    const c = src.match(new RegExp(`const ${ref} =\\s*'([^']*)'`));
    assert.ok(c, `styles.ts has no constant ${ref}`);
    return c[1];
  });
}

// The light and dark text (or background) color of a class string.
function colors(classes: string, prop: 'text' | 'bg'): { light: string; dark: string } {
  const parts = classes.split(/\s+/);
  const pick = (dark: boolean) => {
    const re = new RegExp(`^${dark ? 'dark:' : ''}${prop}-((?:zinc|emerald|sky|amber|red)-\\d+|white)$`);
    const hit = parts.map(p => p.match(re)).find(Boolean);
    return hit ? hit[1] : '';
  };
  return { light: pick(false), dark: pick(true) };
}

const SURFACES = { light: ['white', 'zinc-50'], dark: ['#111114', 'zinc-900'] };

test('small text meets 4.5:1 on light and dark panels', () => {
  for (const name of ['muted', 'faint', 'text', 'strong', 'link', 'th', 'td', 'tdWrap']) {
    const c = colors(token(name), 'text');
    assert.ok(c.light && c.dark, `${name} needs a light and a dark text color: ${token(name)}`);
    for (const bg of SURFACES.light) {
      assert.ok(contrast(c.light, bg) >= 4.5, `${name} ${c.light} on ${bg} = ${contrast(c.light, bg).toFixed(2)}:1`);
    }
    for (const bg of SURFACES.dark) {
      assert.ok(contrast(c.dark, bg) >= 4.5, `${name} dark:${c.dark} on ${bg} = ${contrast(c.dark, bg).toFixed(2)}:1`);
    }
  }
});

test('the selected asset/range option keeps 4.5:1 text contrast', () => {
  const fg = colors(token('segOn'), 'text');
  const bg = colors(token('segOn'), 'bg');
  assert.ok(contrast(fg.light, bg.light) >= 4.5, `light ${fg.light} on ${bg.light} = ${contrast(fg.light, bg.light).toFixed(2)}:1`);
  assert.ok(contrast(fg.dark, bg.dark) >= 4.5, `dark ${fg.dark} on ${bg.dark} = ${contrast(fg.dark, bg.dark).toFixed(2)}:1`);
  assert.ok(read('ui.tsx').includes('cx.segOn'), 'SegmentedControl uses cx.segOn');
});

test('every chart uses baseOption and every tooltip builds HTML through tipHtml', () => {
  const files = readdirSync(dir).filter(f => f.endsWith('.tsx'));
  for (const f of files) {
    const src = read(f);
    if (!src.includes('<ReactECharts')) continue;
    // Each option builder (function …Option) spreads baseOption.
    const builders = [...src.matchAll(/function (\w+Option)\(/g)].map(m => m[1]);
    assert.ok(builders.length > 0, `${f} renders charts without an option builder`);
    for (const b of builders) {
      const body = src.slice(src.indexOf(`function ${b}(`));
      const end = body.indexOf('\nfunction ', 1);
      const fn = end > 0 ? body.slice(0, end) : body;
      assert.ok(fn.includes('baseOption('), `${f}: ${b} does not use baseOption (ARIA, chrome)`);
      const tooltipFormatters = fn.split('tooltip:').slice(1).filter(seg => /formatter:/.test(seg.slice(0, seg.indexOf('\n    },') > 0 ? seg.indexOf('\n    },') : seg.length)));
      for (const seg of tooltipFormatters) {
        assert.ok(seg.includes('tipHtml('), `${f}: ${b} has a tooltip formatter that does not use tipHtml`);
      }
    }
  }
});

test('no raw HTML injection in the dashboard', () => {
  for (const f of readdirSync(dir)) {
    const src = read(f);
    assert.ok(!src.includes('dangerouslySetInnerHTML'), `${f} uses dangerouslySetInnerHTML`);
    assert.ok(!/\.innerHTML\s*=/.test(src), `${f} assigns innerHTML`);
  }
});

test('animated placeholders respect reduced motion', () => {
  for (const f of readdirSync(dir).filter(x => x.endsWith('.tsx'))) {
    for (const line of read(f).split('\n')) {
      if (line.includes('animate-pulse')) assert.ok(line.includes('motion-reduce:animate-none'), `${f}: ${line.trim()}`);
    }
  }
});

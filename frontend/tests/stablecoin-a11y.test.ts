// Keyboard-reachable tabs and selectors (stablecoin.md §7.3): roving focus as
// in the WAI-ARIA Authoring Practices.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { rovingIndex } from '../src/opendata/stablecoin/a11y.ts';
import { safeHref } from '../src/opendata/stablecoin/deeplink.ts';

test('arrow keys move and wrap; Home and End jump', () => {
  assert.equal(rovingIndex('ArrowRight', 0, 6), 1);
  assert.equal(rovingIndex('ArrowRight', 5, 6), 0);
  assert.equal(rovingIndex('ArrowLeft', 0, 6), 5);
  assert.equal(rovingIndex('ArrowDown', 2, 4), 3);
  assert.equal(rovingIndex('ArrowUp', 0, 4), 3);
  assert.equal(rovingIndex('Home', 3, 4), 0);
  assert.equal(rovingIndex('End', 0, 4), 3);
  assert.equal(rovingIndex('Enter', 1, 4), null);
  assert.equal(rovingIndex('ArrowRight', 0, 0), null);
});

test('only http(s) links are rendered as links', () => {
  assert.equal(safeHref('https://tether.to/en/transparency/'), 'https://tether.to/en/transparency/');
  assert.equal(safeHref('http://example.com'), 'http://example.com');
  assert.equal(safeHref('javascript:alert(1)'), undefined);
  assert.equal(safeHref(' JavaScript:alert(1)'), undefined);
  assert.equal(safeHref('data:text/html,<b>x</b>'), undefined);
  assert.equal(safeHref('//evil.example'), undefined);
  assert.equal(safeHref(''), undefined);
  assert.equal(safeHref(undefined), undefined);
});

test('overview range stability and ARIA landmarks/live-region/tabpanel mounts (L13, L15)', () => {
  const dashSrc = readFileSync(new URL('../src/opendata/stablecoin/StablecoinDashboard.tsx', import.meta.url), 'utf8');
  const tickerSrc = readFileSync(new URL('../src/opendata/stablecoin/StablecoinTicker.tsx', import.meta.url), 'utf8');
  const matrixSrc = readFileSync(new URL('../src/opendata/stablecoin/SignalMatrix.tsx', import.meta.url), 'utf8');

  // L13: overviewPoll is keyed on dedicated overviewRange state updated only on the overview tab.
  assert.ok(
    dashSrc.includes('const [overviewRange, setOverviewRange] = useState'),
    'StablecoinDashboard must keep a dedicated overviewRange state so changing Peg/Supply range does not blank overview data',
  );
  // L15: ticker has role="region" alongside aria-label="Live stablecoin ticker".
  assert.ok(
    tickerSrc.includes('role="region"') && tickerSrc.includes('aria-label="Live stablecoin ticker"'),
    'StablecoinTicker must declare role="region" with aria-label',
  );
  // L15: SignalMatrix keeps aria-live="polite" mounted in the DOM before selection.
  assert.ok(
    matrixSrc.includes('<div aria-live="polite">{sel && selected && <SignalDetail'),
    'SignalMatrix must keep aria-live="polite" mounted in the DOM',
  );
  // L15: all tabpanels stay mounted in the DOM so aria-controls always resolves.
  assert.ok(
    dashSrc.includes('{mounted[t.id] ? renderTab(t.id) : null}'),
    'StablecoinDashboard must always mount role="tabpanel" elements so aria-controls resolves',
  );
});

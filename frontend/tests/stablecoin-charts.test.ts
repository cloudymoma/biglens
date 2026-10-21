// Chart tooltips escape every upstream string (stablecoin.md §11, §12.4: the
// lesson from the Bitcoin basis-tooltip XSS fix) and ECharts ARIA is on (§7.3).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { baseOption, colorOf, tipHtml } from '../src/opendata/stablecoin/charts.ts';

const PAYLOAD = '<img src=x onerror=alert(1)>';

test('tooltip header, labels, values and notes are escaped', () => {
  const html = tipHtml(PAYLOAD, [{ label: `kraken ${PAYLOAD}`, value: '"><script>x</script>', color: '#26A17B' }], `note ${PAYLOAD}`);
  assert.ok(!html.includes('<img'), html);
  assert.ok(!html.includes('<script'), html);
  assert.ok(html.includes('&lt;img src=x onerror=alert(1)&gt;'), html);
  assert.ok(html.includes('&quot;&gt;&lt;script&gt;'), html);
  assert.ok(html.includes('background:#26A17B'), html);
});

test('only #hex colors reach the tooltip style', () => {
  const html = tipHtml('h', [{ label: 'a', value: '1', color: 'red;background:url(javascript:alert(1))' }]);
  assert.ok(!html.includes('javascript'), html);
  assert.ok(!html.includes('background:'), html);
  assert.equal(colorOf('#2775CA'), '#2775CA');
  assert.equal(colorOf('rgba(0,0,0,1)'), undefined);
  assert.equal(colorOf({ type: 'linear' }), undefined);
});

test('every chart carries an ARIA description', () => {
  for (const theme of ['light', 'dark'] as const) {
    const o = baseOption(theme, 'USDT consensus deviation, 7 days');
    assert.equal(o.aria.enabled, true);
    assert.equal(o.aria.label.description, 'USDT consensus deviation, 7 days');
  }
});

test('lines break across missing samples instead of interpolating', async () => {
  const { withGaps } = await import('../src/opendata/stablecoin/charts.ts');
  const pts: [string, number | null][] = [
    ['2026-10-20T16:25:00Z', -11],
    ['2026-10-20T16:30:00Z', -10],
    ['2026-10-21T01:40:00Z', -11],
    ['2026-10-21T01:45:00Z', -10],
  ];
  const out = withGaps(pts, p => p[0], 15 * 60_000, at => [at, null] as [string, number | null]);
  assert.equal(out.length, 5);
  assert.deepEqual(out[2], ['2026-10-20T16:30:00Z', null]);
  // Daily points one day apart stay connected.
  const days: [string, number][] = [['2026-10-18', 1], ['2026-10-19', 2]];
  assert.equal(withGaps(days, p => p[0], 2 * 86_400_000, at => [at, 0] as [string, number]).length, 2);
});

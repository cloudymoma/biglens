// Chart tooltips escape every upstream string (stablecoin.md §11, §12.4: the
// lesson from the Bitcoin basis-tooltip XSS fix) and ECharts ARIA is on (§7.3).
import { readFile } from 'node:fs/promises';
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

test('M17: buildRelativePegSeries aligns exchange and Uniswap timestamps to YYYY-MM-DD on daily resolution', async () => {
  const { buildRelativePegSeries } = await import('../src/opendata/stablecoin/pegSeries.ts');

  const daily = buildRelativePegSeries({
    series_resolution: '1d',
    series: [
      { ts: '2026-10-21', consensus: 1.0001, dev_bps: 1, disp_bps: 2, venues_used: 3 },
      { ts: '2026-10-22', consensus: 0.9999, dev_bps: -1, disp_bps: 2, venues_used: 3 },
    ],
    uniswap_series: [
      { ts: '2026-10-21T12:00:00Z', price: 1.0002, dev_bps: 2 },
      { ts: '2026-10-21T23:50:00Z', price: 1.0003, dev_bps: 3 },
      { ts: '2026-10-22T23:50:00Z', price: 0.9998, dev_bps: -2 },
    ],
  });
  assert.equal(daily.daily, true);
  assert.deepEqual(daily.medianData, [
    ['2026-10-21', 1],
    ['2026-10-22', -1],
  ]);
  assert.deepEqual(daily.uniswapData, [
    ['2026-10-21', 3],
    ['2026-10-22', -2],
  ]);

  // Fallback when series already has date-only strings even if resolution label is missing.
  const mixed = buildRelativePegSeries({
    series_resolution: '',
    series: [{ ts: '2026-10-21', consensus: 1, dev_bps: 0, disp_bps: 1, venues_used: 2 }],
    uniswap_series: [{ ts: '2026-10-21T23:50:00Z', price: 1.0001, dev_bps: 1 }],
  });
  assert.equal(mixed.daily, true);
  assert.deepEqual(mixed.uniswapData, [['2026-10-21', 1]]);

  // 5m intraday series (24h, 7d, 30d) retain exact RFC3339 timestamps.
  const intraday = buildRelativePegSeries({
    series_resolution: '5m',
    series: [
      { ts: '2026-10-21T04:55:00Z', consensus: 1.0, dev_bps: 0, disp_bps: 1, venues_used: 3 },
      { ts: '2026-10-21T05:00:00Z', consensus: 1.0001, dev_bps: 1, disp_bps: 1, venues_used: 3 },
    ],
    uniswap_series: [
      { ts: '2026-10-21T04:50:00Z', price: 1.0002, dev_bps: 2 },
      { ts: '2026-10-21T05:00:00Z', price: 0.9999, dev_bps: -1 },
    ],
  });
  assert.equal(intraday.daily, false);
  assert.deepEqual(intraday.medianData, [
    ['2026-10-21T04:55:00Z', 0],
    ['2026-10-21T05:00:00Z', 1],
  ]);
  assert.deepEqual(intraday.uniswapData, [
    ['2026-10-21T04:50:00Z', 2],
    ['2026-10-21T05:00:00Z', -1],
  ]);
});

test('M17: PegTab relOption uses buildRelativePegSeries to plot aligned timestamps', async () => {
  const src = await readFile(new URL('../src/opendata/stablecoin/PegTab.tsx', import.meta.url), 'utf8');
  assert.match(src, /buildRelativePegSeries\(rel\)/);
  assert.match(src, /data:\s*medianData/);
  assert.match(src, /data:\s*uniswapData/);
});

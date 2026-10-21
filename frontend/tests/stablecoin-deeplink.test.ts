// Deep links (stablecoin.md §7.3): asset, tab and range live in the URL query
// and are parsed against the same whitelists as the API (§9); invalid values
// fall back to the defaults.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { effectiveRange, parseView, viewSearch } from '../src/opendata/stablecoin/deeplink.ts';

test('empty query → defaults', () => {
  assert.deepEqual(parseView(''), { asset: 'all', tab: 'overview', range: '90d' });
});

test('valid values are kept', () => {
  assert.deepEqual(parseView('?dataset=stablecoin&asset=eurc&tab=peg&range=30d'), { asset: 'eurc', tab: 'peg', range: '30d' });
  assert.deepEqual(parseView('?asset=usdt&tab=supply&range=all'), { asset: 'usdt', tab: 'supply', range: 'all' });
  assert.deepEqual(parseView('?asset=usdc&tab=controls'), { asset: 'usdc', tab: 'controls', range: null });
});

test('invalid values fall back to the defaults, as the API whitelists do', () => {
  assert.deepEqual(parseView('?asset=dai&tab=hack&range=10y'), { asset: 'all', tab: 'overview', range: '90d' });
  // The API rejects upper-case assets (400), so the dashboard does not accept them either.
  assert.equal(parseView('?asset=USDT').asset, 'all');
  assert.equal(parseView("?asset=usdt'--").asset, 'all');
  assert.equal(parseView('?asset=usdt%27--').asset, 'all');
  assert.equal(parseView('?tab=Peg').tab, 'overview');
});

test('a range is checked against the tab it is used on', () => {
  // 24h exists on /peg only; /supply and /overview accept 30d|90d|1y|all.
  assert.equal(parseView('?tab=peg&range=24h').range, '24h');
  assert.equal(parseView('?tab=supply&range=24h').range, '90d');
  assert.equal(parseView('?tab=overview&range=7d').range, '90d');
  assert.equal(parseView('?tab=peg&range=1y').range, '1y');
  // Tabs without a range selector carry none.
  assert.equal(parseView('?tab=reserves&range=30d').range, null);
  assert.equal(parseView('?tab=chains&range=30d').range, null);
});

test('effectiveRange keeps a selection only where the tab accepts it', () => {
  assert.equal(effectiveRange('peg', '24h'), '24h');
  assert.equal(effectiveRange('overview', '24h'), '90d');
  assert.equal(effectiveRange('supply', '30d'), '30d');
  assert.equal(effectiveRange('peg', null), '7d');
  assert.equal(effectiveRange('controls', '7d'), null);
});

test('viewSearch writes asset/tab/range and keeps unrelated parameters', () => {
  assert.equal(viewSearch({ asset: 'eurc', tab: 'peg', range: '30d' }, '?dataset=stablecoin'),
    '?dataset=stablecoin&asset=eurc&tab=peg&range=30d');
  // A tab without a range drops a stale range parameter.
  assert.equal(viewSearch({ asset: 'usdt', tab: 'chains', range: null }, '?dataset=stablecoin&asset=usdc&range=30d'),
    '?dataset=stablecoin&asset=usdt&tab=chains');
  // Round trip.
  const s = viewSearch({ asset: 'usdc', tab: 'supply', range: '1y' }, '');
  assert.deepEqual(parseView(s), { asset: 'usdc', tab: 'supply', range: '1y' });
});

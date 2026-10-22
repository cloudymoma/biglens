// Stablecoin formatters: null renders "—", never 0 (stablecoin.md §7, §12.1);
// units are always visible (§7.3).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  fmtBps,
  fmtDateTime,
  fmtMoney,
  fmtNative,
  fmtPrice,
  fmtShortAddr,
  fmtTimeHM,
  fmtEurcBool,
  computeEurMarketStats,
} from '../src/opendata/stablecoin/format.ts';

test('missing values render as a dash, never 0', () => {
  for (const v of [null, undefined, Number.NaN]) {
    assert.equal(fmtBps(v), '—');
    assert.equal(fmtPrice(v, 'USD'), '—');
    assert.equal(fmtNative(v, 'USDT'), '—');
    assert.equal(fmtMoney(v, 'EUR'), '—');
  }
  assert.equal(fmtTimeHM(''), '—');
  assert.equal(fmtTimeHM(undefined), '—');
  assert.equal(fmtTimeHM('not a time'), '—');
  assert.equal(fmtDateTime(undefined), '—');
  assert.equal(fmtShortAddr(null), '—');
});

test('bps carry a sign and the unit', () => {
  assert.equal(fmtBps(-11.24), '-11.2 bps');
  assert.equal(fmtBps(3), '+3.0 bps');
  assert.equal(fmtBps(0), '0.0 bps');
  assert.equal(fmtBps(-1947.4, 0), '-1,947 bps');
  assert.equal(fmtBps(12.345, 2, false), '12.35 bps');
});

test('prices are in the asset numéraire', () => {
  assert.equal(fmtPrice(0.99987, 'USD'), '$0.99987');
  assert.equal(fmtPrice(1.00012, 'EUR'), '€1.00012');
  assert.equal(fmtPrice(1.0001, 'USDT'), '1.00010 USDT');
});

test('native amounts are compact with the token unit', () => {
  assert.equal(fmtNative(183_545_469_669.19, 'USDT'), '183.55B USDT');
  assert.equal(fmtNative(440_120_000, 'EURC'), '440.12M EURC');
  assert.equal(fmtNative(-2.5e9, 'USDC'), '-2.50B USDC');
  assert.equal(fmtNative(1234.5, 'USDT'), '1,235 USDT');
  assert.equal(fmtNative(12.5, 'USDT'), '12.50 USDT');
});

test('money uses $ or €', () => {
  assert.equal(fmtMoney(1.2e9, 'EUR'), '€1.20B');
  assert.equal(fmtMoney(312.6e9, 'USD'), '$312.60B');
  assert.equal(fmtMoney(-5e6, 'USD'), '-$5.00M');
  assert.equal(fmtMoney(0.0123, 'USD', 4), '$0.0123');
});

test('times are UTC', () => {
  assert.equal(fmtTimeHM('2026-10-19T13:20:00Z'), '13:20 UTC');
  assert.equal(fmtDateTime('2026-10-19T03:05:00Z'), '2026-10-19 03:05 UTC');
});

test('addresses are shortened, not altered', () => {
  assert.equal(fmtShortAddr('0xdac17f958d2ee523a2206206994597c13d831ec7'), '0xdac1…1ec7');
  assert.equal(fmtShortAddr('TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t'), 'TR7NHq…Lj6t');
  assert.equal(fmtShortAddr('short'), 'short');
});

test('watchlist MarketTable renders row notes alongside — for unreliable aggregator prices', () => {
  const dir = dirname(fileURLToPath(import.meta.url));
  const overviewSrc = readFileSync(join(dir, '../src/opendata/stablecoin/OverviewTab.tsx'), 'utf8');
  assert.ok(
    overviewSrc.includes('r.note') && overviewSrc.includes('fmtBps(r.dev_bps)'),
    'MarketTable must render r.note when a watchlist row has an unreliable aggregator price',
  );
});

test('PegTab Depeg days table shows close deviation, VWAP deviation, and classified deviation, and last deviation names threshold and source', () => {
  const dir = dirname(fileURLToPath(import.meta.url));
  const pegSrc = readFileSync(join(dir, '../src/opendata/stablecoin/PegTab.tsx'), 'utf8');
  assert.ok(
    pegSrc.includes('vwap_dev_bps') && pegSrc.includes('VWAP deviation') && pegSrc.includes('source_label'),
    'PegTab must render VWAP deviation and classified deviation in Depeg days and source_label/threshold_bps in the last deviation headline',
  );
});

test('absent EURC booleans render as a dash, never no (M5)', () => {
  assert.equal(fmtEurcBool(undefined, 'S1 is capped at warning'), '—');
  assert.equal(fmtEurcBool(null, 'S1 is capped at warning'), '—');
  assert.equal(fmtEurcBool(false, 'S1 is capped at warning'), 'no: S1 is capped at warning');
  assert.equal(fmtEurcBool(true, 'S1 is capped at warning'), 'yes');
  assert.equal(fmtEurcBool(undefined, undefined, 'derived inputs share the FX venues'), '—');
  assert.equal(fmtEurcBool(false, undefined, 'derived inputs share the FX venues'), 'no');
  assert.equal(fmtEurcBool(true, undefined, 'derived inputs share the FX venues'), 'yes: derived inputs share the FX venues');
  const dir = dirname(fileURLToPath(import.meta.url));
  const pegSrc = readFileSync(join(dir, '../src/opendata/stablecoin/PegTab.tsx'), 'utf8');
  assert.ok(pegSrc.includes('fmtEurcBool('), 'PegTab EurcMode must use fmtEurcBool');
});

test('EUR market share and total come from USD supply, and native total shows a dash when any EUR coin lacks native supply (M6)', () => {
  const partial = computeEurMarketStats([
    { core: 'eurc', numeraire: 'EUR', circulating_native: 400_000_000, circulating_usd: 440_000_000 },
    { numeraire: 'EUR', circulating_native: null, circulating_usd: 110_000_000 },
  ]);
  assert.equal(partial.sharePct, '80.0%');
  assert.equal(partial.totalUsd, '$550.00M');
  assert.equal(partial.nativeTotal, '—');
  assert.equal(partial.nativeSub, '1 of 2 coins have native supply');

  const complete = computeEurMarketStats([
    { core: 'eurc', numeraire: 'EUR', circulating_native: 400_000_000, circulating_usd: 440_000_000 },
    { numeraire: 'EUR', circulating_native: 100_000_000, circulating_usd: 110_000_000 },
  ]);
  assert.equal(complete.sharePct, '80.0%');
  assert.equal(complete.nativeTotal, '€500.00M');
  assert.equal(complete.nativeSub, '2 coins');

  const dir = dirname(fileURLToPath(import.meta.url));
  const supplySrc = readFileSync(join(dir, '../src/opendata/stablecoin/SupplyTab.tsx'), 'utf8');
  assert.ok(supplySrc.includes('computeEurMarketStats('), 'SupplyTab must use computeEurMarketStats');
});

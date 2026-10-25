// BigQuery switch (off by default) and opt-in on-chain activity panels
// (stablecoin.md §7.2, §9, §10, §11).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  nextStableBigQueryPreference,
  resolveStableBigQueryShow,
  STORAGE_KEY,
} from '../src/opendata/stablecoin/useStableBigQuery.ts';

test('BigQuery switch is off by default even when SQLite data is available', () => {
  assert.equal(STORAGE_KEY, 'biglens.stablecoin.showBigQuery');
  // Off by default when pref is null (never toggled by the user).
  assert.equal(resolveStableBigQueryShow(null, false), false);
  assert.equal(resolveStableBigQueryShow(null, true), false);
  // Only shown when both available and explicitly enabled by the user.
  assert.equal(resolveStableBigQueryShow(false, true), false);
  assert.equal(resolveStableBigQueryShow(true, true), true);
  assert.equal(resolveStableBigQueryShow(true, false), false);

  // First toggle from default (null) turns it on; second toggle turns it off.
  assert.equal(nextStableBigQueryPreference(null), true);
  assert.equal(nextStableBigQueryPreference(true), false);
  assert.equal(nextStableBigQueryPreference(false), true);
});

test('StablecoinDashboard, ChainsTab and ControlsTab wire BigQuery switch and on-chain panels', () => {
  const dashSrc = readFileSync(new URL('../src/opendata/stablecoin/StablecoinDashboard.tsx', import.meta.url), 'utf8');
  const chainsSrc = readFileSync(new URL('../src/opendata/stablecoin/ChainsTab.tsx', import.meta.url), 'utf8');
  const controlsSrc = readFileSync(new URL('../src/opendata/stablecoin/ControlsTab.tsx', import.meta.url), 'utf8');
  const lockedSrc = readFileSync(new URL('../src/opendata/stablecoin/StableBigQueryLocked.tsx', import.meta.url), 'utf8');

  assert.ok(
    dashSrc.includes('useStableBigQuery') &&
      dashSrc.includes('StableBigQuerySwitch') &&
      dashSrc.includes('StableBigQueryStatusChip'),
    'StablecoinDashboard must render the BigQuery status chip and switch (off by default)',
  );
  assert.ok(
    lockedSrc.includes('--stablecoin-bq-resume') && lockedSrc.includes('Show BigQuery panels'),
    'StableBigQueryLocked must include the switch, status chip with --stablecoin-bq-resume, and locked state card',
  );
  assert.ok(
    chainsSrc.includes('StableBigQueryLocked') &&
      chainsSrc.includes('fetchStableOnchain') &&
      chainsSrc.includes('Large transfers') &&
      chainsSrc.includes('coverage_note'),
    'ChainsTab must gate BigQuery on-chain activity behind bqControl.show and render daily rollups + large transfers when enabled',
  );
  assert.ok(
    controlsSrc.includes('c.tx_hash'),
    'ControlsTab change log must display tx_hash when present on BigQuery-sourced control events',
  );
});

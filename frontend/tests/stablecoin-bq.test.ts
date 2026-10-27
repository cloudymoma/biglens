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

test('M8: status chip is an accessible button with aria-expanded that opens the BigQuery jobs panel, and disabled switch shows visible text + button', () => {
  const dashSrc = readFileSync(new URL('../src/opendata/stablecoin/StablecoinDashboard.tsx', import.meta.url), 'utf8');
  const lockedSrc = readFileSync(new URL('../src/opendata/stablecoin/StableBigQueryLocked.tsx', import.meta.url), 'utf8');

  // Chip is a real <button> with aria-expanded that opens the jobs panel.
  assert.ok(
    lockedSrc.includes('aria-expanded={open}') &&
      lockedSrc.includes('StableBigQueryJobsPanel') &&
      dashSrc.includes('StableBigQueryJobsPanel'),
    'StableBigQueryStatusChip must be a real <button> with aria-expanded that toggles StableBigQueryJobsPanel',
  );

  // Disabled switch shows visible reason text and a button that opens the jobs panel.
  assert.ok(
    lockedSrc.includes('No on-chain data yet: run a job once, or turn on the schedule.') &&
      lockedSrc.includes('onOpenJobs'),
    'Disabled Show BigQuery panels switch must render visible explanation text and a button to open the jobs panel',
  );

  // Jobs panel includes schedule on/off with conf.yaml snippet + copy button, budget bar, 00:00 UTC note, and native <dialog> confirm modal.
  assert.ok(
    lockedSrc.includes('on (hourly)') &&
      lockedSrc.includes('off: set') &&
      lockedSrc.includes('stablecoin.bigquery.jobs: true') &&
      lockedSrc.includes('New day available after 00:00 UTC') &&
      lockedSrc.includes('Budget this month:') &&
      lockedSrc.includes('<dialog') &&
      lockedSrc.includes('aria-live') &&
      lockedSrc.includes('BigQuery bills actual bytes scanned.') &&
      lockedSrc.includes('If the fresh dry-run estimate grows by more than 10% before confirmation, the run is refused.'),
    'StableBigQueryJobsPanel must render schedule status, copyable conf.yaml snippet, budget bar, 00:00 UTC note, native <dialog>, and aria-live status',
  );

  // Fast 5s polling while any job is running.
  assert.ok(
    dashSrc.includes('5_000') && dashSrc.includes('.running'),
    'StablecoinDashboard must poll /meta every 5 s while a BigQuery job is running',
  );
});

test('M8: confirm flow calls estimate then run with X-BigLens-Action: stablecoin-bq, times are UTC, and missing values render —', async () => {
  const {
    fmtBytesGiB,
    fmtNextCheckUTC,
    fmtLastRunUTC,
    mapStableBQError,
  } = await import('../src/opendata/stablecoin/format.ts');
  const {
    estimateStableBQJob,
    runStableBQJob,
  } = await import('../src/opendata/stablecoin/api.ts');

  // Missing values render "—".
  assert.equal(fmtBytesGiB(null), '—');
  assert.equal(fmtBytesGiB(undefined), '—');
  assert.equal(fmtBytesGiB(Number.NaN), '—');
  assert.equal(fmtBytesGiB(1073741824), '1.00 GiB');
  assert.equal(fmtNextCheckUTC(null, '2026-10-19T13:21:00Z'), '—');
  assert.equal(fmtNextCheckUTC(undefined, '2026-10-19T13:21:00Z'), '—');
  assert.equal(fmtLastRunUTC(null), '—');
  assert.equal(fmtLastRunUTC({}), '—');

  // Times are UTC.
  assert.equal(
    fmtNextCheckUTC('2026-10-19T14:00:00Z', '2026-10-19T13:21:00Z'),
    '2026-10-19 14:00 UTC (in 39m)',
  );
  assert.equal(
    fmtLastRunUTC({
      last_run_at: '2026-10-19T13:21:00Z',
      last_run_trigger: 'manual',
      last_run_result: 'ok',
      last_run_bytes_billed: 943718400,
    }),
    '2026-10-19 13:21 UTC · manual · ok · 0.88 GiB',
  );

  // Plain error mapping for 403, 409, 429.
  assert.match(mapStableBQError(403, ''), /this computer|loopback|127\.0\.0\.1/i);
  assert.match(mapStableBQError(429, 'rate limited: wait 180s'), /5 minutes|wait 180s/i);
  assert.match(mapStableBQError(409, 'estimate changed by more than 10%'), /estimate changed/i);

  // Confirm flow calls estimate first, then run with confirmed_estimate_bytes and X-BigLens-Action: stablecoin-bq.
  const calls: { url: string; body: unknown; headers: Record<string, string> }[] = [];
  const fakePost = async <T>(url: string, body: unknown, config?: { headers?: Record<string, string> }): Promise<{ data: T }> => {
    calls.push({ url, body, headers: config?.headers ?? {} });
    if (url.includes('/bigquery/estimate')) {
      return {
        data: {
          job: 'stable_eth',
          window_from: '2026-10-18',
          window_to: '2026-10-18',
          estimated_bytes: 1048576000,
          estimated_usd: 0.00596,
          pricing_note: 'Estimated at $6.25/TiB; BigQuery bills actual bytes scanned.',
          budget_used_bytes: 0,
          budget_limit_bytes: 549755813888,
          fits_budget: true,
          nothing_to_do: false,
        } as T,
      };
    }
    return { data: { status: 'started', job: 'stable_eth' } as T };
  };

  const est = await estimateStableBQJob('stable_eth', fakePost);
  assert.equal(est.estimated_bytes, 1048576000);
  const runRes = await runStableBQJob('stable_eth', est.estimated_bytes, fakePost);
  assert.equal(runRes.status, 'started');

  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, '/api/opendata/stablecoin/bigquery/estimate?job=stable_eth');
  assert.equal(calls[0].headers['X-BigLens-Action'], 'stablecoin-bq');
  assert.equal(calls[0].headers['Content-Type'], 'application/json');
  assert.equal(calls[1].url, '/api/opendata/stablecoin/bigquery/run?job=stable_eth');
  assert.deepEqual(calls[1].body, { confirmed_estimate_bytes: 1048576000 });
  assert.equal(calls[1].headers['X-BigLens-Action'], 'stablecoin-bq');
  assert.equal(calls[1].headers['Content-Type'], 'application/json');
});

test('M8: README Stablecoins section documents the BigQuery jobs panel, Run now, cost note, and loopback-only rule', () => {
  const readmeSrc = readFileSync(new URL('../../README.md', import.meta.url), 'utf8');
  const stableIdx = readmeSrc.indexOf('### Stablecoins');
  const nextSectionIdx = readmeSrc.indexOf('### Crypto Pulse', stableIdx);
  const stableSection = readmeSrc.slice(stableIdx, nextSectionIdx);

  assert.ok(
    stableSection.includes('BigQuery jobs') &&
      stableSection.includes('Run now') &&
      stableSection.includes('$6.25/TiB') &&
      stableSection.includes('127.0.0.1') &&
      stableSection.includes('X-BigLens-Action: stablecoin-bq'),
    'README Stablecoins section must document the BigQuery jobs panel, Run now flow, $6.25/TiB cost note, and loopback-only rule',
  );
});



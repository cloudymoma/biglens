// Bitcoin BigQuery jobs panel, Run now flow and loopback-only rule (the same
// model as the Stablecoin BigQuery jobs panel).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const readBitcoinSrc = (file: string): string =>
  readFileSync(new URL(`../src/opendata/bitcoin/${file}`, import.meta.url), 'utf8');

test('Bitcoin status chip is an accessible button that opens the BigQuery jobs panel, and the disabled switch says how to get data', () => {
  const dashSrc = readBitcoinSrc('BitcoinDashboard.tsx');
  const chipSrc = readBitcoinSrc('BigQueryStatusChip.tsx');
  const switchSrc = readBitcoinSrc('BigQuerySwitch.tsx');

  assert.ok(
    chipSrc.includes('<button') &&
      chipSrc.includes('aria-expanded={open}') &&
      chipSrc.includes('aria-controls="btc-bq-jobs-panel"') &&
      dashSrc.includes('BigQueryJobsPanel') &&
      dashSrc.includes('onToggle'),
    'BigQueryStatusChip must be a real <button> with aria-expanded that toggles BigQueryJobsPanel',
  );
  assert.ok(
    switchSrc.includes('No on-chain data yet: run a job once, or turn on the schedule.') &&
      switchSrc.includes('Open jobs panel') &&
      dashSrc.includes('onOpenJobs'),
    'The disabled Show BigQuery panels switch must explain how to get data and offer a button that opens the jobs panel',
  );
  // /meta polls every 5 s while a job runs, then the tabs reload once.
  assert.ok(
    dashSrc.includes('5_000') && dashSrc.includes('.running'),
    'BitcoinDashboard must poll /meta every 5 s while a BigQuery job is running',
  );
});

test('Bitcoin jobs panel renders schedule status with conf.yaml snippets, the budget bar, the 06:30 UTC settle note and a native <dialog>; the UTXO baseline is status-only', () => {
  const panelSrc = readBitcoinSrc('BigQueryJobsPanel.tsx');

  assert.ok(
    panelSrc.includes('on (hourly)') &&
      panelSrc.includes('every ${cadenceDays} days') &&
      panelSrc.includes('off: set') &&
      panelSrc.includes('bitcoin.bigquery.jobs: true') &&
      panelSrc.includes('bitcoin.bigquery.coin_age.enabled: true') &&
      panelSrc.includes('06:30 UTC') &&
      panelSrc.includes('Budget this month:') &&
      panelSrc.includes('<dialog') &&
      panelSrc.includes('aria-live') &&
      panelSrc.includes('Reserved while running') &&
      panelSrc.includes('If the fresh dry-run estimate grows by more than 10% before confirmation, the run is refused.') &&
      panelSrc.includes('--bitcoin-bq-resume'),
    'BigQueryJobsPanel must render schedule status, copyable snippets, budget bar, settle note, reservation, native <dialog> and aria-live status',
  );

  // Only Job A and Job B get Run now; Job C (~687 GiB, 30-day lock) shows its
  // status and CLI command.
  const runnable = panelSrc.match(/const RUNNABLE_JOBS = \[([^\]]*)\]/);
  assert.ok(runnable, 'BigQueryJobsPanel must declare RUNNABLE_JOBS');
  assert.equal(runnable[1].replace(/\s/g, ''), "'btc_onchain','btc_coinage'");
  assert.ok(
    panelSrc.includes('btc_utxo_baseline') && panelSrc.includes('Not run from this panel') && panelSrc.includes('--yes'),
    'The UTXO baseline row must show its CLI commands and say it does not run from the panel',
  );
});

test('confirm flow calls estimate then run with X-BigLens-Action: bitcoin-bq, times are UTC, and missing values render —', async () => {
  const { fmtBytesGiB, fmtLastRunUTC, fmtNextCheckUTC, mapBitcoinBQError, settledThroughUTC } = await import(
    '../src/opendata/bitcoin/format.ts'
  );
  const { estimateBitcoinBQJob, runBitcoinBQJob } = await import('../src/opendata/bitcoin/api.ts');

  // Missing values render "—".
  assert.equal(fmtBytesGiB(null), '—');
  assert.equal(fmtBytesGiB(undefined), '—');
  assert.equal(fmtBytesGiB(Number.NaN), '—');
  assert.equal(fmtBytesGiB(1073741824), '1.00 GiB');
  assert.equal(fmtNextCheckUTC(null, '2026-10-19T13:21:00Z'), '—');
  assert.equal(fmtLastRunUTC(null), '—');
  assert.equal(fmtLastRunUTC({}), '—');
  assert.equal(settledThroughUTC(undefined), null);

  // Times are UTC; the weekly coin-age check is days away.
  assert.equal(fmtNextCheckUTC('2026-10-19T14:00:00Z', '2026-10-19T13:21:00Z'), '2026-10-19 14:00 UTC (in 39m)');
  assert.equal(fmtNextCheckUTC('2026-10-26T07:00:00Z', '2026-10-19T14:00:00Z'), '2026-10-26 07:00 UTC (in 6d 17h)');
  assert.equal(
    fmtLastRunUTC({
      last_run_at: '2026-10-19T13:21:00Z',
      last_run_trigger: 'manual',
      last_run_result: 'ok',
      last_run_bytes_billed: 555745280,
    }),
    '2026-10-19 13:21 UTC · manual · ok · 0.52 GiB',
  );

  // A UTC day settles at 06:30 UTC the next day: when the next settle is
  // 2026-10-20 06:30, manual runs can fetch through 2026-10-18.
  assert.equal(settledThroughUTC('2026-10-20T06:30:00Z'), '2026-10-18');

  // Plain error mapping for 403, 409 and 429.
  assert.match(mapBitcoinBQError(403, ''), /this computer|loopback|127\.0\.0\.1/i);
  assert.match(mapBitcoinBQError(429, 'rate limited: wait 180s'), /5 minutes|wait 180s/i);
  assert.match(mapBitcoinBQError(409, 'estimate changed by more than 10%'), /estimate changed/i);
  assert.match(mapBitcoinBQError(409, 'nothing to do: already synced through the latest settled UTC day'), /settled/i);
  assert.match(mapBitcoinBQError(409, 'over_budget: estimated run exceeds remaining monthly budget'), /budget/i);

  // Estimate first, then run with confirmed_estimate_bytes and the bitcoin-bq action header.
  const calls: { url: string; body: unknown; headers: Record<string, string> }[] = [];
  const fakePost = async <T>(url: string, body: unknown, config?: { headers?: Record<string, string> }): Promise<{ data: T }> => {
    calls.push({ url, body, headers: config?.headers ?? {} });
    if (url.includes('/bigquery/estimate')) {
      return {
        data: {
          job: 'btc_coinage',
          window_from: '2026-10-16',
          window_to: '2026-10-18',
          estimated_bytes: 107374182400,
          estimated_usd: 0.61,
          reserve_bytes: 134217728000,
          pricing_note: 'Estimated at $6.25/TiB; BigQuery bills actual bytes scanned.',
          budget_used_bytes: 0,
          budget_limit_bytes: 2199023255552,
          fits_budget: true,
          nothing_to_do: false,
        } as T,
      };
    }
    return { data: { status: 'started', job: 'btc_coinage' } as T };
  };

  const est = await estimateBitcoinBQJob('btc_coinage', fakePost);
  assert.equal(est.estimated_bytes, 107374182400);
  const runRes = await runBitcoinBQJob('btc_coinage', est.estimated_bytes, fakePost);
  assert.equal(runRes.status, 'started');

  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, '/api/opendata/bitcoin/bigquery/estimate?job=btc_coinage');
  assert.equal(calls[0].headers['X-BigLens-Action'], 'bitcoin-bq');
  assert.equal(calls[0].headers['Content-Type'], 'application/json');
  assert.equal(calls[1].url, '/api/opendata/bitcoin/bigquery/run?job=btc_coinage');
  assert.deepEqual(calls[1].body, { confirmed_estimate_bytes: 107374182400 });
  assert.equal(calls[1].headers['X-BigLens-Action'], 'bitcoin-bq');
  assert.equal(calls[1].headers['Content-Type'], 'application/json');
});


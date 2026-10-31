import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { historyMissingLatestTx } from '../src/opendata/crypto/paymentCheck.ts';

const tabUrl = new URL('../src/opendata/crypto/PaymentCheckTab.tsx', import.meta.url);

test('historyMissingLatestTx detects when 7d history lags behind /live latest payment (R2)', () => {
  // Missing recent payment inside 7d window -> retry needed.
  assert.equal(
    historyMissingLatestTx(
      { since: '2026-10-20T00:00:00Z', txs: [{ tx_hash: '0xold' } as never] },
      { tx_hash: '0xNEW', timestamp: '2026-10-26T12:00:00Z' },
    ),
    true,
  );

  // Unconfirmed / unsettled tx without block timestamp -> brand new, retry needed.
  assert.equal(
    historyMissingLatestTx(
      { since: '2026-10-20T00:00:00Z', txs: [] },
      { tx_hash: 'mempool_0conf', timestamp: '' },
    ),
    true,
  );

  // Already present (case-insensitive hash match) -> no retry.
  assert.equal(
    historyMissingLatestTx(
      { since: '2026-10-20T00:00:00Z', txs: [{ tx_hash: '0xnew' } as never] },
      { tx_hash: '0xNEW', timestamp: '2026-10-26T12:00:00Z' },
    ),
    false,
  );

  // Older than 7d `since` cutoff (e.g. getPayRecent on idle BTC/TRON address) -> outside history window, no retry loop.
  assert.equal(
    historyMissingLatestTx(
      { since: '2026-10-20T00:00:00Z', txs: [] },
      { tx_hash: 'old_30d_tx', timestamp: '2026-09-25T00:00:00Z' },
    ),
    false,
  );

  // Truncated 1,000-row history: tx at or older than oldest returned row must NOT trigger endless poll refetches.
  assert.equal(
    historyMissingLatestTx(
      {
        since: '2026-10-20T00:00:00Z',
        scope: { truncated: true },
        txs: [
          { tx_hash: '0xrow1', timestamp: '2026-10-26T12:10:00Z' } as never,
          { tx_hash: '0xrow1000', timestamp: '2026-10-26T12:00:00Z' } as never,
        ],
      },
      { tx_hash: '0xrow1001', timestamp: '2026-10-26T11:55:00Z' },
    ),
    false,
  );

  // Truncated 1,000-row history: brand-new tx newer than oldest returned row MUST still trigger refresh.
  assert.equal(
    historyMissingLatestTx(
      {
        since: '2026-10-20T00:00:00Z',
        scope: { truncated: true },
        txs: [
          { tx_hash: '0xrow1', timestamp: '2026-10-26T12:10:00Z' } as never,
          { tx_hash: '0xrow1000', timestamp: '2026-10-26T12:00:00Z' } as never,
        ],
      },
      { tx_hash: '0xbrand_new', timestamp: '2026-10-26T12:12:00Z' },
    ),
    true,
  );
});

test('PaymentCheckTab avoids stuck historyLoading on poll error, avoids self-aborting slow retries, and refreshes stale initial history (R2)', () => {
  const src = readFileSync(tabUrl, 'utf8');
  // Must not have a catch block on fetchPaymentHistory that omits setHistoryLoading(false).
  const historyFetchCalls = src.split('fetchPaymentHistory(').length - 1;
  assert.equal(
    historyFetchCalls,
    1,
    `PaymentCheckTab should funnel all fetchPaymentHistory calls through one shared loader so error/loading cleanup is consistent (found ${historyFetchCalls})`,
  );
  assert.ok(
    src.includes('histInFlightRef'),
    'PaymentCheckTab must track in-flight history request so 5s polling does not repeatedly abort slow >5s history retries',
  );
  assert.ok(
    src.includes('missingRetryHashRef'),
    'PaymentCheckTab initial loadLive must also refresh history once when warm cached history is missing d.latest',
  );
});

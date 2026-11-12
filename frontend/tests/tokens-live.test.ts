import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  barSupplyValue,
  formatSupplySummary,
  summarizeLiveSupply,
} from '../src/opendata/crypto/tokensLive.ts';
import type { TokenSupplyRow } from '../src/types.ts';

const row = (overrides: Partial<TokenSupplyRow>): TokenSupplyRow => ({
  asset: 'USDT',
  network: 'eth',
  network_label: 'Ethereum',
  contract: '0xdac17f958d2ee523a2206206994597c13d831ec7',
  tier: 'native',
  label: 'USDT',
  supply: 0,
  updated_at: '2026-11-11T08:00:00Z',
  ...overrides,
});

test('H8: summarizeLiveSupply marks totals as partial when any chain row has an error instead of counting it as 0', () => {
  const rows: TokenSupplyRow[] = [
    row({ asset: 'USDT', network: 'tron', network_label: 'TRON', supply: 80e9 }),
    row({ asset: 'USDT', network: 'eth', network_label: 'Ethereum', supply: 0, error: 'rate_limited' }),
    row({ asset: 'USDC', network: 'eth', network_label: 'Ethereum', supply: 50e9 }),
    row({ asset: 'USDC', network: 'sol', network_label: 'Solana', supply: 10e9 }),
    row({ asset: 'EURC', network: 'eth', network_label: 'Ethereum', supply: 0, error: 'timeout' }),
  ];

  const summary = summarizeLiveSupply(rows);

  assert.equal(summary.USDT.total, 80e9);
  assert.equal(summary.USDT.partial, true);
  assert.equal(summary.USDT.knownRows, 1);
  assert.equal(summary.USDT.failedRows, 1);
  assert.deepEqual(summary.USDT.failedNetworks, ['Ethereum']);

  assert.equal(summary.USDC.total, 60e9);
  assert.equal(summary.USDC.partial, false);
  assert.equal(summary.USDC.knownRows, 2);
  assert.equal(summary.USDC.failedRows, 0);

  // When every row for an asset failed, total must be null (unavailable), not 0.
  assert.equal(summary.EURC.total, null);
  assert.equal(summary.EURC.partial, true);
  assert.equal(summary.EURC.knownRows, 0);
  assert.equal(summary.EURC.failedRows, 1);

  assert.equal(barSupplyValue(rows, 'USDT', 'eth', 'native'), null);
  assert.equal(barSupplyValue(rows, 'USDT', 'tron', 'native'), 80e9);

  assert.match(formatSupplySummary('USDT', '$', summary.USDT), /80\.00B.*partial/i);
  assert.match(formatSupplySummary('EURC', '€', summary.EURC), /—|unavailable/i);
});

test('H8: summarizeLiveSupply marks totals as partial when any chain row has a non-finite supply without an error', () => {
  const rows: TokenSupplyRow[] = [
    row({ asset: 'USDT', network: 'tron', network_label: 'TRON', supply: 80e9 }),
    row({ asset: 'USDT', network: 'eth', network_label: 'Ethereum', supply: Number.NaN }),
    row({ asset: 'USDC', network: 'sol', network_label: 'Solana', supply: 50e9 }),
    row({ asset: 'USDC', network: 'arb', network_label: 'Arbitrum', supply: Number.POSITIVE_INFINITY }),
    row({ asset: 'USDC', network: 'op', network_label: 'Optimism', supply: null as unknown as number }),
    row({ asset: 'EURC', network: 'eth', network_label: 'Ethereum', supply: Number.NaN }),
  ];

  const summary = summarizeLiveSupply(rows);

  assert.equal(summary.USDT.total, 80e9);
  assert.equal(summary.USDT.partial, true);
  assert.equal(summary.USDT.knownRows, 1);
  assert.equal(summary.USDT.failedRows, 1);
  assert.deepEqual(summary.USDT.failedNetworks, ['Ethereum']);

  assert.equal(summary.USDC.total, 50e9);
  assert.equal(summary.USDC.partial, true);
  assert.equal(summary.USDC.knownRows, 1);
  assert.equal(summary.USDC.failedRows, 2);
  assert.deepEqual(summary.USDC.failedNetworks, ['Arbitrum', 'Optimism']);

  assert.equal(summary.EURC.total, null);
  assert.equal(summary.EURC.partial, true);
  assert.equal(summary.EURC.knownRows, 0);
  assert.equal(summary.EURC.failedRows, 1);
  assert.deepEqual(summary.EURC.failedNetworks, ['Ethereum']);

  assert.match(formatSupplySummary('USDT', '$', summary.USDT), /80\.00B.*partial/i);
  assert.match(formatSupplySummary('EURC', '€', summary.EURC), /—|unavailable/i);
});

test('H8: TokensLivePanel uses summarizeLiveSupply and renders live.warnings', () => {
  const src = readFileSync(new URL('../src/opendata/crypto/TokensLivePanel.tsx', import.meta.url), 'utf8');
  assert.ok(
    src.includes('summarizeLiveSupply('),
    'TokensLivePanel must use summarizeLiveSupply instead of summing raw supply rows blindly',
  );
  assert.ok(
    src.includes('live.warnings'),
    'TokensLivePanel must render live.warnings when present',
  );
});

// CryptoDashboard deep-link mounting: opening #pay must mount only the initial
// tab so PulseTab does not fire a background /api/opendata/crypto/pulse request.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const cryptoDashboardUrl = new URL('../src/opendata/crypto/CryptoDashboard.tsx', import.meta.url);

test('CryptoDashboard initializes visited with only initTab', () => {
  const src = readFileSync(cryptoDashboardUrl, 'utf8');
  assert.ok(
    src.includes('useState<ReadonlySet<TabId>>(new Set<TabId>([initTab]))'),
    'visited must initialize as new Set<TabId>([initTab]) so #pay does not mount PulseTab',
  );
  assert.ok(
    !src.includes("['pulse', initTab]"),
    "visited must not eagerly include 'pulse' when initTab is another tab",
  );
});

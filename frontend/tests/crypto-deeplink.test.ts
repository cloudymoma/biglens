// CryptoDashboard deep-link mounting: opening #pay must mount only the initial
// tab so PulseTab does not fire a background /api/opendata/crypto/pulse request.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const cryptoDashboardUrl = new URL('../src/opendata/crypto/CryptoDashboard.tsx', import.meta.url);
const controlsTabUrl = new URL('../src/opendata/stablecoin/ControlsTab.tsx', import.meta.url);

test('CryptoDashboard initializes visited with only initTab and supports #risk deep link (L11)', () => {
  const src = readFileSync(cryptoDashboardUrl, 'utf8');
  assert.ok(
    src.includes('useState<ReadonlySet<TabId>>(new Set<TabId>([initTab]))'),
    'visited must initialize as new Set<TabId>([initTab]) so #pay does not mount PulseTab',
  );
  assert.ok(
    !src.includes("['pulse', initTab]"),
    "visited must not eagerly include 'pulse' when initTab is another tab",
  );
  assert.ok(
    src.includes("startsWith('#risk')") && src.includes("return 'risk'"),
    'initialCryptoTab must route #risk to the Address Risk tab',
  );
  const controlsSrc = readFileSync(controlsTabUrl, 'utf8');
  assert.ok(
    controlsSrc.includes("${datasetHref('crypto')}#risk"),
    'ControlsTab must link to Crypto Pulse -> Address Risk with #risk',
  );
});

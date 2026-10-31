import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const scamRadarPanelUrl = new URL('../src/opendata/crypto/ScamRadarPanel.tsx', import.meta.url);

test('ScamRadarPanel clarifies fake-token table shows contracts active in last 7d with cumulative (90d) totals', () => {
  const src = readFileSync(scamRadarPanelUrl, 'utf8');
  assert.ok(
    !src.includes('Top fake stablecoin contracts (last 7d, ETH)'),
    'must not label 90-day cumulative fake-token totals simply as "(last 7d, ETH)"',
  );
  assert.ok(
    src.includes('active last 7d') && src.includes('90d'),
    'must state that selection is active in last 7d while counts are cumulative (90d retention)',
  );
});

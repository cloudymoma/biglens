// Live ticker (stablecoin.md §7.1, §7.4): the 24h deviation range is labeled
// "since HH:MM" until 24 h of samples exist.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fmtRangeWindow } from '../src/opendata/stablecoin/format.ts';

test('the 24h range window is labeled by its first sample while it is partial', () => {
  assert.equal(fmtRangeWindow('2026-10-19T13:20:00Z', true), 'since 13:20 UTC');
  assert.equal(fmtRangeWindow('2026-10-18T13:25:00Z', false), '24h');
  // No sample yet: nothing to qualify (the range itself renders "—").
  assert.equal(fmtRangeWindow(undefined, false), '24h');
  assert.equal(fmtRangeWindow(undefined, true), '24h');
});

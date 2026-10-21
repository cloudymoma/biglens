// CSV export on every table (stablecoin.md §7.3): each <table> the dashboard
// renders has a CsvButton in the same panel, or in the same component when the
// table is a reusable panel body (SignalMatrix, ControlTable).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';

const dir = new URL('../src/opendata/stablecoin/', import.meta.url);

test('every table has a CSV export next to it', () => {
  const missing: string[] = [];
  for (const f of readdirSync(dir).filter(x => x.endsWith('.tsx'))) {
    const src = readFileSync(new URL(f, dir), 'utf8');
    let at = src.indexOf('<table');
    while (at >= 0) {
      const before = src.slice(0, at);
      const boundary = Math.max(before.lastIndexOf('<Panel'), before.lastIndexOf('\nfunction '), before.lastIndexOf('\nexport default function '));
      const scope = before.slice(Math.max(boundary, 0));
      const line = before.split('\n').length;
      if (!scope.includes('<CsvButton')) missing.push(`${f}:${line}`);
      at = src.indexOf('<table', at + 1);
    }
  }
  assert.deepEqual(missing, [], `tables without a CSV export in their panel: ${missing.join(', ')}`);
});

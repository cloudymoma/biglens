// CSV export on every table (stablecoin.md §7.3): each <table> the dashboard
// renders has a CsvButton in the same panel, or in the same component when the
// table is a reusable panel body (SignalMatrix, ControlTable).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { downloadCsv } from '../src/opendata/common/format.ts';

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

test('downloadCsv neutralises formula strings, preserves negative numbers, quotes CR, and prepends a UTF-8 BOM', async () => {
  let capturedParts: BlobPart[] = [];
  const origBlob = globalThis.Blob;
  const origURL = globalThis.URL;
  const origDoc = (globalThis as Record<string, unknown>).document;
  try {
    globalThis.Blob = class extends origBlob {
      constructor(parts?: BlobPart[], options?: BlobPropertyBag) {
        super(parts, options);
        capturedParts = parts ?? [];
      }
    } as typeof Blob;
    URL.createObjectURL = () => 'blob:test';
    URL.revokeObjectURL = () => {};
    (globalThis as Record<string, unknown>).document = {
      createElement: () => ({ href: '', download: '', click() {} }),
    };

    downloadCsv(
      'test.csv',
      ['name', 'dev_bps', 'note'],
      [
        ['=HYPERLINK("http://evil.example","x")', -3.2, 'line1\rline2'],
        ['+SUM(A1:A2)', 0, '\tleading-tab'],
        ['@cmd', null, '\rleading-cr'],
      ],
    );
  } finally {
    globalThis.Blob = origBlob;
    globalThis.URL = origURL;
    (globalThis as Record<string, unknown>).document = origDoc;
  }

  const csv = capturedParts.map(String).join('');
  assert.ok(csv.startsWith('\uFEFF'), 'CSV must start with UTF-8 BOM');
  const lines = csv.slice(1).split('\n');
  assert.equal(lines[0], 'name,dev_bps,note');
  assert.equal(lines[1], `"'=HYPERLINK(""http://evil.example"",""x"")",-3.2,"line1\rline2"`);
  assert.equal(lines[2], `'+SUM(A1:A2),0,'\tleading-tab`);
  assert.equal(lines[3], `'@cmd,,"'\rleading-cr"`);
});

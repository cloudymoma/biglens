// Keyboard-reachable tabs and selectors (stablecoin.md §7.3): roving focus as
// in the WAI-ARIA Authoring Practices.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { rovingIndex } from '../src/opendata/stablecoin/a11y.ts';
import { safeHref } from '../src/opendata/stablecoin/deeplink.ts';

test('arrow keys move and wrap; Home and End jump', () => {
  assert.equal(rovingIndex('ArrowRight', 0, 6), 1);
  assert.equal(rovingIndex('ArrowRight', 5, 6), 0);
  assert.equal(rovingIndex('ArrowLeft', 0, 6), 5);
  assert.equal(rovingIndex('ArrowDown', 2, 4), 3);
  assert.equal(rovingIndex('ArrowUp', 0, 4), 3);
  assert.equal(rovingIndex('Home', 3, 4), 0);
  assert.equal(rovingIndex('End', 0, 4), 3);
  assert.equal(rovingIndex('Enter', 1, 4), null);
  assert.equal(rovingIndex('ArrowRight', 0, 0), null);
});

test('only http(s) links are rendered as links', () => {
  assert.equal(safeHref('https://tether.to/en/transparency/'), 'https://tether.to/en/transparency/');
  assert.equal(safeHref('http://example.com'), 'http://example.com');
  assert.equal(safeHref('javascript:alert(1)'), undefined);
  assert.equal(safeHref(' JavaScript:alert(1)'), undefined);
  assert.equal(safeHref('data:text/html,<b>x</b>'), undefined);
  assert.equal(safeHref('//evil.example'), undefined);
  assert.equal(safeHref(''), undefined);
  assert.equal(safeHref(undefined), undefined);
});

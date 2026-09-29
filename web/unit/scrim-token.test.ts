import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';

const src = join(import.meta.dirname, '..', 'src', 'lib');
const read = (path: string): string => readFileSync(join(src, path), 'utf8');

// A veil retuned in one overlay and not the others is a Dialog opened over a
// Drawer dimming the page by two different amounts. The recipe lives in the
// token file, once, and each overlay names it.
const OVERLAYS = [
  'components/Dialog.svelte',
  'components/Drawer.svelte',
  'shell/CommandPalette.svelte',
  'shell/NavMenu.svelte',
];

test('the modal scrim is a token, defined once', () => {
  const tokens = read('styles/tokens.css');
  assert.match(tokens, /--z-scrim:\s*color-mix\(in srgb, var\(--z-bg\) 72%, transparent\);/);
  assert.match(tokens, /--z-scrim-blur:\s*3px;/);
});

for (const overlay of OVERLAYS) {
  test(`${overlay} takes its scrim from the token rather than its own copy`, () => {
    const source = read(overlay);
    assert.match(source, /background:\s*var\(--z-scrim\);/);
    assert.match(source, /backdrop-filter:\s*blur\(var\(--z-scrim-blur\)\);/);
    assert.doesNotMatch(source, /color-mix\(/);
  });
}

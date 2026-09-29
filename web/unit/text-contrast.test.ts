import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';

const css = readFileSync(
  join(import.meta.dirname, '..', 'src', 'lib', 'styles', 'tokens.css'),
  'utf8',
);

/** The declarations of the first block that opens with `selector`. */
function block(selector: string): string {
  const start = css.indexOf(`${selector} {`);
  assert.notEqual(start, -1, `${selector} is in tokens.css`);
  return css.slice(start, css.indexOf('\n}', start));
}

function token(scope: string, name: string): string {
  const found = new RegExp(`${name}:\\s*(#[0-9a-fA-F]{6});`).exec(scope);
  assert.ok(found, `${name} is a hex value in its block`);
  return found[1] as string;
}

function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  }) as [number, number, number];
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
}

// Subtle is the weakest text in the product -- timestamps, breadcrumbs,
// placeholders -- and it sits on the page ground and inside sunken wells and
// hovered rows as often as on a white card. AA is 4.5:1 on each of them, not
// on the best one.
const LIGHT_SURFACES = ['--z-brand-white', '--z-bg', '--z-surface-sunken', '--z-surface-hover'];

test('subtle text reaches AA on every light surface it sits on', () => {
  const light = block(':root');
  const subtle = token(light, '--z-text-subtle');
  for (const surface of LIGHT_SURFACES.map((name) => token(light, name))) {
    assert.ok(
      contrast(subtle, surface) >= 4.5,
      `${subtle} on ${surface} is ${contrast(subtle, surface).toFixed(2)}:1`,
    );
  }
});

test('subtle text is still visibly weaker than muted text in the light theme', () => {
  const light = block(':root');
  const onGround = token(light, '--z-bg');
  const muted = contrast(token(light, '--z-text-muted'), onGround);
  const subtle = contrast(token(light, '--z-text-subtle'), onGround);
  assert.ok(
    muted - subtle >= 0.75,
    `muted ${muted.toFixed(2)}:1 against subtle ${subtle.toFixed(2)}:1`,
  );
});

test('subtle text reaches AA on every dark surface it sits on', () => {
  const dark = block(":root[data-theme='dark']");
  const subtle = token(dark, '--z-text-subtle');
  for (const name of ['--z-bg', '--z-surface', '--z-surface-raised', '--z-surface-sunken']) {
    const surface = token(dark, name);
    assert.ok(
      contrast(subtle, surface) >= 4.5,
      `${subtle} on ${surface} is ${contrast(subtle, surface).toFixed(2)}:1`,
    );
  }
});

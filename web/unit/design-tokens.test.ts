import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';

const src = join(import.meta.dirname, '..', 'src');

function* sources(dir: string): Generator<string> {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) yield* sources(path);
    else if (/\.(svelte|css|ts)$/.test(entry.name) && !entry.name.endsWith('.d.ts')) yield path;
  }
}

// A var() naming a token that does not exist is not an error anywhere: the
// declaration is dropped at computed-value time, so `--z-radius-pill` quietly
// draws square corners and `--z-weight-regular` inherits whatever the parent
// had. Nothing else catches a misspelt token, so this does.
test('every design token a component reads is defined somewhere', () => {
  const used: [string, string][] = [];
  const defined = new Set<string>();
  for (const path of sources(src)) {
    const text = readFileSync(path, 'utf8');
    // A name built by interpolation (`--z-radius-${size}`) is checked by
    // whatever chooses the suffix, not here.
    for (const [, name] of text.matchAll(/var\(\s*(--z-[a-z0-9-]+)/g)) {
      if (!name!.endsWith('-')) used.push([name!, path]);
    }
    // A component may declare a local property for its own children, in a
    // rule or through style:--z-x; both count as a definition.
    for (const [, name] of text.matchAll(/(?:^|[\s;{"'`]|style:)(--z-[a-z0-9-]+)\s*[:=]/g)) {
      defined.add(name!);
    }
  }
  const missing = [...used].filter(([name]) => !defined.has(name));
  assert.deepEqual(
    missing.map(([name, path]) => `${name} (${path.slice(src.length + 1)})`),
    [],
  );
});

import assert from 'node:assert/strict';
import { join } from 'node:path';
import test from 'node:test';
import { findLostSpaces, lostSpacesUnder } from '../scripts/block-spacing.mjs';

// Svelte drops the whitespace at the start and at the end of a block's body, so a
// sentence that continues across an {#if} loses the space written inside it. The
// markup looks right and the page reads "in use· 1 via Tailcat". These are the
// shapes that reached the pages, and the ones that must stay quiet: a check that
// cries wolf on the cases below would be switched off the first time it did.

const flagged = (markup: string) => findLostSpaces(markup).length;

test('a space written at the start of a block is a lost space when text comes before the block', () => {
  const shapes: Record<string, string> = {
    'a summary line that continues':
      '<p>{n} hosts · {used} of {all} slots\n  in use{#if t}\n    · {k} via Tailcat{/if}\n</p>',
    'a sentence that continues':
      '<dd>resized.{#if dind}\n  Its runner and its daemon share that slot.{/if}</dd>',
    'a word that continues': '<p>{who} stopped it{#if since}\n  on {when}{/if}.</p>',
    'the other branch of an if': '<p>lands.{#if a}x{:else}\n  Twice over{/if}</p>',
    'a branch after an else if': '<p>lands.{#if a}x{:else if b}\n  Twice over{/if}</p>',
    'an expression that starts the block': '<p>since{#if a}\n  {when}{/if}</p>',
    'a loop': '<p>on{#each hosts as h}\n  {h.name}{/each}</p>',
    'text after an element': '<p><strong>stopped</strong>{#if a}\n  by somebody{/if}</p>',
    'text after an expression': '<p>{n}{#if a}\n  hosts{/if}</p>',
    'a block inside an element inside a block':
      '<div>{#if a}<p>in use{#if b}\n  · more{/if}</p>{/if}</div>',
  };
  for (const [name, markup] of Object.entries(shapes)) {
    assert.equal(flagged(markup), 1, `${name} should be flagged once: ${markup}`);
  }
});

test('a space written at the end of a block is a lost space when text follows the block', () => {
  const shapes: Record<string, string> = {
    'a prefix and a word': '<time>{#if prefix}{prefix}\n  {/if}{text}</time>',
    'words and a word': '<p>{#if a}created \n{/if}just now</p>',
    'words and an element': '<p>{#if a}created \n{/if}<time>now</time></p>',
  };
  for (const [name, markup] of Object.entries(shapes)) {
    assert.equal(flagged(markup), 1, `${name} should be flagged once: ${markup}`);
  }
});

test('the fixes this check was written for are not flagged', () => {
  const fixed: Record<string, string> = {
    'the space before the block': '<p>in use {#if t}\n  · {k} via Tailcat{/if}\n</p>',
    'the space after the block': '<p>{#if a}created{/if} just now</p>',
    'the whole phrase in one expression': "<p>{who} stopped it{since ? ` on ${since}` : ''}.</p>",
    'a separating line break outside the block': '<p>{n} hosts\n  {#if a}\n    · more{/if}\n</p>',
    'a block that starts its element': '<p>{#if a}\n  Version {v}{/if}\n</p>',
    'a block that ends its element': '<p>Version {v}\n  {#if a}by somebody\n  {/if}</p>',
  };
  for (const [name, markup] of Object.entries(fixed)) {
    assert.equal(flagged(markup), 0, `${name} should not be flagged: ${markup}`);
  }
});

test('punctuation that belongs against its neighbour is allowed to lose its space', () => {
  const quiet: Record<string, string> = {
    'an opening bracket before the block': '<p>({#if a}\n  foo{/if})</p>',
    'a full stop that starts the block': '<p>foo{#if a}\n  .{/if}</p>',
    'a comma after the block': '<p>{#if a}\n  foo\n{/if}, then</p>',
    'a full stop after the block': '<p>{#if a}foo\n{/if}.</p>',
    'a closing bracket after the block': '<p>({#if a}foo \n{/if})</p>',
  };
  for (const [name, markup] of Object.entries(quiet)) {
    assert.equal(flagged(markup), 0, `${name} should not be flagged: ${markup}`);
  }
});

test('a block with nothing to lose, or nothing touching it, is not flagged', () => {
  const quiet: Record<string, string> = {
    'markup only': '<div>{#if a}\n  <span>x</span>\n{/if}</div>',
    'a comment before it': '<p>word <!-- note -->{#if a}\n  more{/if}</p>',
    'a block level element before it': '<div>x</div>\n{#if a}\n  <p>more</p>\n{/if}',
    'an each with a fallback':
      '<ul>{#each xs as x}\n  <li>{x}</li>\n{:else}\n  <li>none</li>\n{/each}</ul>',
  };
  for (const [name, markup] of Object.entries(quiet)) {
    assert.equal(flagged(markup), 0, `${name} should not be flagged: ${markup}`);
  }
});

test('the finding says where it is', () => {
  const [found] = findLostSpaces('<p>\n  in use{#if a}\n    · more{/if}\n</p>');
  assert.equal(found?.line, 2);
  assert.match(found?.message ?? '', /put the space before the block/);
});

// The whole point: the pages as they are read the way the compiler reads them.
test('no page in the UI continues a sentence across the edge of a block', () => {
  const found = lostSpacesUnder(join(import.meta.dirname, '..', 'src'));
  assert.deepEqual(
    found.map((f) => `${f.file}:${f.line}`),
    [],
  );
});

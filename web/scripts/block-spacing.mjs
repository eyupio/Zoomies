// Finds the sentences a Svelte template joins without a space.
//
// Svelte drops the whitespace at the start and at the end of a block's body, so a
// space written just inside `{#if}`, or just before `{/if}`, is not there when the
// page runs. Text that continues across a block reads fine in the markup and comes
// out as "in use· 1 via Tailcat", "lands.Twice over" or "stopped iton 8 Oct". It
// reached the pages six times in one change and once before that, and nothing in
// the source looks wrong, so this reads the template the way the compiler does and
// says where.
//
// The fix is always the same: put the space in the text around the block, where
// it is kept, or make the optional part one string so there is no edge to trim.
//
// Run as `node scripts/block-spacing.mjs`; `npm run lint` does.
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { parse } from 'svelte/compiler';

// Punctuation that hugs the word before it, and punctuation that hugs the word
// after it. A trimmed space next to either is the one nobody wanted: "{#if x}\n
// foo{/if})" is meant to read "(foo)".
const CLOSING = /[.,;:!?)\]}%'"’”]/u;
const OPENING = /[([{'"‘“/-]/u;

// A stand-in for "some word-like thing": an expression or an element has no first
// or last character the template can see, and is taken to be one that needs a gap.
const WORD = 'x';

/** The nodes of each branch of a block, or null for a node that is not a block. */
function branches(node) {
  switch (node.type) {
    case 'IfBlock': {
      const out = [node.consequent.nodes];
      const rest = node.alternate?.nodes;
      if (rest) {
        // `{:else if}` is an IfBlock inside the alternate; its branches belong to
        // the same block, and sit between the same neighbours.
        if (rest.length === 1 && rest[0].type === 'IfBlock' && rest[0].elseif) {
          out.push(...branches(rest[0]));
        } else {
          out.push(rest);
        }
      }
      return out;
    }
    case 'EachBlock':
      return [node.body.nodes, node.fallback?.nodes].filter(Boolean);
    case 'KeyBlock':
      return [node.fragment.nodes];
    case 'AwaitBlock':
      return [node.pending?.nodes, node.then?.nodes, node.catch?.nodes].filter(Boolean);
    default:
      return null;
  }
}

/** The character just outside a block on its left, or null if a space is there already. */
function before(prev) {
  if (!prev || prev.type === 'Comment') return null;
  if (prev.type === 'Text') return /\S$/.test(prev.data) ? prev.data.at(-1) : null;
  return WORD;
}

/** The character just outside a block on its right, or null if a space is there already. */
function after(next) {
  if (!next || next.type === 'Comment') return null;
  if (next.type === 'Text') return /^\S/.test(next.data) ? next.data[0] : null;
  return WORD;
}

function lostAtStart(body) {
  const first = body[0];
  if (first?.type !== 'Text' || !/^\s/.test(first.data)) return null;
  const text = first.data.trimStart();
  if (text) return text[0];
  // Only whitespace, then an expression: "{#if x}\n {y}{/if}".
  return body[1]?.type === 'ExpressionTag' ? WORD : null;
}

function lostAtEnd(body) {
  const last = body.at(-1);
  if (last?.type !== 'Text' || !/\s$/.test(last.data)) return null;
  const text = last.data.trimEnd();
  if (text) return text.at(-1);
  const earlier = body.at(-2);
  return earlier && earlier.type !== 'Comment' && earlier.type !== 'Text' ? WORD : null;
}

/**
 * The places in a template where a space is written inside a block's edge and
 * would be lost, between text that needs it.
 *
 * @param {string} source the contents of a .svelte file
 * @returns {{ line: number, column: number, message: string }[]}
 */
export function findLostSpaces(source) {
  const found = [];
  const where = (offset) => {
    const upTo = source.slice(0, offset);
    const line = upTo.split('\n').length;
    return { line, column: offset - upTo.lastIndexOf('\n') };
  };

  const visit = (siblings) => {
    siblings.forEach((node, i) => {
      for (const body of branches(node) ?? []) {
        const left = before(siblings[i - 1]);
        const startsWith = lostAtStart(body);
        if (left && startsWith && !OPENING.test(left) && !CLOSING.test(startsWith)) {
          found.push({
            ...where(node.start),
            message:
              'the space at the start of this block is dropped by Svelte, so the text before it and the text in it run together; put the space before the block',
          });
        }
        const right = after(siblings[i + 1]);
        const endsWith = lostAtEnd(body);
        if (right && endsWith && !CLOSING.test(right)) {
          found.push({
            ...where(node.end),
            message:
              'the space at the end of this block is dropped by Svelte, so the text in it and the text after it run together; put the space after the block',
          });
        }
      }
      for (const key of [
        'fragment',
        'body',
        'consequent',
        'alternate',
        'pending',
        'then',
        'catch',
        'fallback',
      ]) {
        if (node[key]?.nodes) visit(node[key].nodes);
      }
    });
  };

  visit(parse(source, { modern: true }).fragment.nodes);
  return found;
}

function svelteFiles(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return svelteFiles(path);
    return entry.name.endsWith('.svelte') ? [path] : [];
  });
}

/**
 * Every lost space in the .svelte files under a directory.
 *
 * @param {string} dir
 * @returns {{ file: string, line: number, column: number, message: string }[]}
 */
export function lostSpacesUnder(dir) {
  return svelteFiles(dir).flatMap((file) =>
    findLostSpaces(readFileSync(file, 'utf8')).map((found) => ({
      file: relative(dir, file),
      ...found,
    })),
  );
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const problems = lostSpacesUnder(join(import.meta.dirname, '..', 'src'));
  for (const { file, line, column, message } of problems) {
    console.error(`src/${file}:${line}:${column}  ${message}`);
  }
  if (problems.length) {
    const n = problems.length;
    console.error(`\n${n} place${n === 1 ? '' : 's'} where Svelte will drop a space.`);
    process.exit(1);
  }
}

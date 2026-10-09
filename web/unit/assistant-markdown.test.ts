import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  inlines,
  parseMarkdown,
  safeHref,
  type Block,
  type Inline,
} from '../src/lib/assistant/markdown.ts';

/** Every string a tree holds, so a test can say what text survived and in what order. */
function words(node: Block | Inline | Block[] | Inline[]): string {
  if (Array.isArray(node)) return node.map((n) => words(n)).join('');
  switch (node.t) {
    case 'text':
      return node.v;
    case 'code':
      return node.v;
    case 'br':
      return '\n';
    case 'hr':
      return '';
    case 'a':
    case 'strong':
    case 'em':
    case 'p':
    case 'h':
    case 'quote':
      return words(node.c);
    case 'list':
      return node.items.map((i) => words(i)).join('|');
    case 'table':
      return [node.head, ...node.rows].map((r) => r.map((c) => words(c)).join(',')).join(';');
  }
}

test('an answer is paragraphs, and a single newline inside one is a line break', () => {
  const tree = parseMarkdown('First line\nsecond line\n\nNext paragraph');
  assert.equal(tree.length, 2);
  assert.deepEqual(tree[0], {
    t: 'p',
    c: [{ t: 'text', v: 'First line' }, { t: 'br' }, { t: 'text', v: 'second line' }],
  });
});

test('bold, italic and code are marked, and what is inside keeps its own marks', () => {
  const [p] = parseMarkdown('Use **`GET /api/runners`** or *carefully* __this__ and _that_.');
  assert.ok(p && p.t === 'p');
  const kinds = p.c.map((n) => n.t);
  assert.deepEqual(kinds, ['text', 'strong', 'text', 'em', 'text', 'strong', 'text', 'em', 'text']);
  const strong = p.c[1]!;
  assert.ok(strong.t === 'strong');
  assert.deepEqual(strong.c, [{ t: 'code', v: 'GET /api/runners' }]);
});

test('an underscore inside a word is part of the word', () => {
  assert.equal(
    words(inlines('set snake_case_name and my_var_ok')),
    'set snake_case_name and my_var_ok',
  );
  assert.ok(inlines('snake_case_name').every((n) => n.t === 'text'));
});

test('a mark that never closes, or is not hugging its text, stays as the characters it was', () => {
  assert.equal(words(inlines('half **open and `unclosed')), 'half **open and `unclosed');
  assert.equal(words(inlines('2 * 3 * 4')), '2 * 3 * 4');
  assert.ok(inlines('2 * 3 * 4').every((n) => n.t === 'text'));
  assert.equal(words(inlines('**')), '**');
  assert.equal(words(inlines('****')), '****');
});

test('a backslash makes the next mark an ordinary character', () => {
  const tree = inlines('\\*not bold\\* and 5 \\* 3');
  assert.ok(tree.every((n) => n.t === 'text'));
  assert.equal(words(tree), '*not bold* and 5 * 3');
});

test('headings of every depth draw as one of three, and keep their words', () => {
  const tree = parseMarkdown('# One\n## Two\n###### Six');
  assert.deepEqual(
    tree.map((b) => (b.t === 'h' ? b.level : 0)),
    [1, 2, 3],
  );
  assert.equal(words(tree), 'OneTwoSix');
});

test('bullets, numbers and a list inside a list', () => {
  const [bullets] = parseMarkdown('- a\n- b\n  - b1\n  - b2\n- c');
  assert.ok(bullets && bullets.t === 'list' && !bullets.ordered);
  assert.equal(bullets.items.length, 3);
  const inner = bullets.items[1]!.find((b) => b.t === 'list');
  assert.ok(inner && inner.t === 'list');
  assert.equal(inner.items.length, 2);

  const [numbered] = parseMarkdown('3. three\n4. four');
  assert.ok(numbered && numbered.t === 'list' && numbered.ordered);
  assert.equal(numbered.start, 3);
  assert.equal(numbered.items.length, 2);
});

test('a list with a blank line between items is one list, and prose after it is not in it', () => {
  const tree = parseMarkdown('- a\n\n- b\n\nThen a paragraph.');
  assert.equal(tree.length, 2);
  assert.ok(tree[0]!.t === 'list' && tree[0]!.items.length === 2);
  assert.ok(tree[1]!.t === 'p');
});

test('numbers after bullets are a list of their own, and bullets after numbers too', () => {
  const afterBlank = parseMarkdown('- a\n- b\n\n1. one\n2. two');
  assert.deepEqual(
    afterBlank.map((b) => (b.t === 'list' ? (b.ordered ? 'ol' : 'ul') : b.t)),
    ['ul', 'ol'],
  );
  const direct = parseMarkdown('1. one\n2. two\n- x\n- y');
  assert.deepEqual(
    direct.map((b) => (b.t === 'list' ? (b.ordered ? 'ol' : 'ul') : b.t)),
    ['ol', 'ul'],
  );
  // Nested numbers under a bullet are still that bullet's.
  const [nested] = parseMarkdown('- a\n  1. inner\n  2. inner');
  assert.ok(nested && nested.t === 'list' && nested.items.length === 1);
  assert.ok(nested.items[0]!.some((b) => b.t === 'list' && b.ordered));
});

test('a bold phrase at the start of a line is not a bullet', () => {
  const [p] = parseMarkdown('**Where to look:** the dashboard');
  assert.ok(p && p.t === 'p');
  assert.ok(p.c[0]!.t === 'strong');
});

test('fenced code is kept as written, and one still being typed is code, not prose', () => {
  const [closed] = parseMarkdown('```yaml\nruns-on: [self-hosted]\n  # **not bold**\n```');
  assert.deepEqual(closed, {
    t: 'code',
    lang: 'yaml',
    v: 'runs-on: [self-hosted]\n  # **not bold**',
  });
  const [open] = parseMarkdown('```\nstill *arriving');
  assert.deepEqual(open, { t: 'code', lang: '', v: 'still *arriving' });
  // A longer fence is not closed by a shorter run inside it.
  const [long] = parseMarkdown('````\n```\ninner\n```\n````');
  assert.ok(long && long.t === 'code' && long.v === '```\ninner\n```');
});

test('a quote holds blocks, and a table has a header, alignment and rows of the same width', () => {
  const [quote] = parseMarkdown('> a **note**\n> second');
  assert.ok(quote && quote.t === 'quote');
  const [table] = parseMarkdown(
    '| Pool | Idle | Busy |\n|:--|--:|:-:|\n| ci | 2 | 1 |\n| big | 0 |',
  );
  assert.ok(table && table.t === 'table');
  assert.deepEqual(table.align, ['left', 'right', 'center']);
  assert.equal(table.rows.length, 2);
  assert.ok(table.rows.every((r) => r.length === 3));
  assert.equal(words(table), 'Pool,Idle,Busy;ci,2,1;big,0,');
});

test('a line with a pipe and no separator row is a paragraph', () => {
  const [p] = parseMarkdown('use a | b to choose');
  assert.ok(p && p.t === 'p');
});

test('only http and https addresses become links, whatever they are called', () => {
  const [p] = parseMarkdown(
    '[good](https://example.com/a?b=1) [bad](javascript:alert(1)) [data](data:text/html,x) [mixed](JaVaScRiPt:alert(1)) [spaced](java script:alert(1))',
  );
  assert.ok(p && p.t === 'p');
  const links = p.c.filter((n) => n.t === 'a');
  assert.equal(links.length, 1);
  assert.equal(links[0]!.t === 'a' && links[0]!.href, 'https://example.com/a?b=1');
  assert.match(words(p), /\[bad\]\(javascript:alert\(1\)\)/);
  assert.match(words(p), /\[mixed\]/);
});

test('a scheme hidden by a control character is not a link', () => {
  assert.equal(safeHref('java\nscript:alert(1)'), undefined);
  assert.equal(safeHref('\u0001javascript:alert(1)'), undefined);
  assert.equal(safeHref('//example.com'), undefined);
  assert.equal(safeHref('mailto:a@example.com'), undefined);
  assert.equal(safeHref('ftp://example.com'), undefined);
  assert.equal(safeHref('http://example.com'), 'http://example.com/');
});

test('an address with a space or a control character in it is not a link at all', () => {
  assert.equal(safeHref('https://example.com/a b'), undefined);
  assert.equal(safeHref('https://exa\nmple.com'), undefined);
  assert.equal(safeHref('https://example.com/\u0085x'), undefined);
});

test('an underscore or a star that does not hug its text is a character', () => {
  assert.ok(inlines('foo_bar baz_').every((n) => n.t === 'text'));
  assert.ok(inlines('a * b* c').every((n) => n.t === 'text'));
  assert.ok(inlines('a *b * c').every((n) => n.t === 'text'));
  assert.ok(inlines('nothttps://example.com/x').every((n) => n.t === 'text'));
});

test('a bare address is a link without the punctuation that ends the sentence', () => {
  const tree = inlines('See https://docs.zoomies.sh/runners, then (https://example.com/x).');
  const links = tree.filter((n) => n.t === 'a');
  assert.deepEqual(
    links.map((l) => (l.t === 'a' ? l.href : '')),
    ['https://docs.zoomies.sh/runners', 'https://example.com/x'],
  );
  assert.equal(words(tree), 'See https://docs.zoomies.sh/runners, then (https://example.com/x).');
});

test('markup a model writes is text: tags and handlers stay as the characters they are', () => {
  const hostile = '<script>alert(1)</script> <img src=x onerror=alert(1)> **<b>x</b>**';
  const tree = parseMarkdown(hostile);
  assert.equal(words(tree), hostile.replace('**<b>x</b>**', '<b>x</b>'));
  const text = JSON.stringify(tree);
  // The tree is data of a few known shapes; nothing in it is an element or a handler.
  assert.ok(!/"t":"(script|img|html)"/.test(text));
});

test('nesting is bounded, so a hostile answer cannot grow the tree without limit', () => {
  const quotes = parseMarkdown('> '.repeat(200) + 'deep');
  let depth = 0;
  let node: Block | undefined = quotes[0];
  while (node && node.t === 'quote') {
    depth++;
    node = node.c[0];
  }
  assert.ok(depth <= 6, `quote depth ${depth}`);
  assert.match(words(quotes), /deep/);

  const lists = parseMarkdown(
    Array.from({ length: 60 }, (_, i) => ' '.repeat(i * 2) + '- x').join('\n'),
  );
  assert.ok(JSON.stringify(lists).length < 200_000);
  const emphasis = inlines('*'.repeat(500) + 'x' + '*'.repeat(500));
  assert.ok(emphasis.length > 0);
});

test('a very long answer is parsed in time', () => {
  const started = Date.now();
  parseMarkdown('word *emph* `code` [l](https://e.com) '.repeat(20_000));
  parseMarkdown('**a '.repeat(5_000));
  parseMarkdown('| a | b |\n|--|--|\n'.concat('| 1 | 2 |\n'.repeat(5_000)));
  assert.ok(Date.now() - started < 5_000, 'took too long');
});

test('an empty or blank answer is no blocks', () => {
  assert.deepEqual(parseMarkdown(''), []);
  assert.deepEqual(parseMarkdown('\n  \n\n'), []);
});

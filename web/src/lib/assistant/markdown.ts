/**
 * The Markdown a model writes, as a tree the page draws itself.
 *
 * What a model writes is not markup the page should trust, so nothing here
 * produces HTML: it returns a small tree of plain data, and the component that
 * draws it makes an element for each node and puts every string in as text. A
 * thing this does not understand stays on the page as the characters it was
 * written with, which is also what a half-finished answer looks like while it is
 * still being streamed (an unclosed `**` or fence is text until it closes).
 *
 * The subset is what models actually write in an answer: paragraphs, headings,
 * lists (one level inside another), quotes, fenced code, tables, rules, and
 * inline bold, italic, code and links. A link is kept only if it is http or
 * https; `javascript:`, `data:` and the rest are left as their words.
 */

export type Inline =
  | { t: 'text'; v: string }
  | { t: 'br' }
  | { t: 'strong'; c: Inline[] }
  | { t: 'em'; c: Inline[] }
  | { t: 'code'; v: string }
  | { t: 'a'; href: string; c: Inline[] };

export type Align = 'left' | 'center' | 'right' | undefined;

export type Block =
  | { t: 'p'; c: Inline[] }
  | { t: 'h'; level: 1 | 2 | 3; c: Inline[] }
  | { t: 'code'; lang: string; v: string }
  | { t: 'list'; ordered: boolean; start: number; items: Block[][] }
  | { t: 'quote'; c: Block[] }
  | { t: 'hr' }
  | { t: 'table'; head: Inline[][]; align: Align[]; rows: Inline[][][] };

/** How deep quotes and lists may nest. A hostile answer cannot grow the tree without limit. */
const MAX_DEPTH = 4;

const FENCE = /^ {0,3}(`{3,}|~{3,})\s*([\w+#.-]*)[^`]*$/;
const HEADING = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const RULE = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/;
const QUOTE = /^ {0,3}>\s?(.*)$/;
const BULLET = /^( *)([-*+])\s+(.*)$/;
const NUMBERED = /^( *)(\d{1,9})[.)]\s+(.*)$/;
const TABLE_SEPARATOR = /^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)*\|?\s*$/;

export function parseMarkdown(source: string): Block[] {
  return blocks(source.replace(/\r\n?/g, '\n').split('\n'), 0);
}

function blocks(lines: string[], depth: number): Block[] {
  const out: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i]!;
    if (line.trim() === '') {
      i++;
      continue;
    }

    const fence = FENCE.exec(line);
    if (fence) {
      // A fence with no closing line runs to the end: that is what an answer
      // looks like while its code is still arriving, and it is code, not prose.
      const mark = fence[1]!;
      const body: string[] = [];
      i++;
      while (i < lines.length && !closes(lines[i]!, mark)) body.push(lines[i++]!);
      i++;
      out.push({ t: 'code', lang: fence[2] ?? '', v: body.join('\n') });
      continue;
    }

    const heading = HEADING.exec(line);
    if (heading) {
      // A model's "# Title" is not the page's outline: all six levels draw as
      // three small ones, below the page's own headings.
      const level = Math.min(heading[1]!.length, 3) as 1 | 2 | 3;
      out.push({ t: 'h', level, c: inlines(heading[2]!) });
      i++;
      continue;
    }

    if (RULE.test(line)) {
      out.push({ t: 'hr' });
      i++;
      continue;
    }

    if (QUOTE.test(line)) {
      const inner: string[] = [];
      while (i < lines.length && QUOTE.test(lines[i]!)) inner.push(QUOTE.exec(lines[i++]!)![1]!);
      out.push({ t: 'quote', c: depth < MAX_DEPTH ? blocks(inner, depth + 1) : plain(inner) });
      continue;
    }

    if (isTable(lines, i)) {
      const head = cells(lines[i]!);
      const align = cells(lines[i + 1]!).map(alignment);
      i += 2;
      const rows: Inline[][][] = [];
      while (i < lines.length && lines[i]!.trim() !== '' && lines[i]!.includes('|')) {
        rows.push(
          Array.from({ length: head.length }, (_, k) => inlines(cells(lines[i]!)[k] ?? '')),
        );
        i++;
      }
      out.push({ t: 'table', head: head.map(inlines), align, rows });
      continue;
    }

    if (BULLET.test(line) || NUMBERED.test(line)) {
      const taken = takeList(lines, i);
      i = taken.next;
      out.push(list(taken.lines, depth));
      continue;
    }

    // A paragraph runs until a blank line or the start of another block.
    const text: string[] = [];
    while (
      i < lines.length &&
      lines[i]!.trim() !== '' &&
      (text.length === 0 || !startsBlock(lines, i))
    ) {
      text.push(lines[i++]!);
    }
    out.push({ t: 'p', c: inlines(text.join('\n')) });
  }
  return out;
}

function closes(line: string, mark: string): boolean {
  const t = line.trim();
  return t.length >= mark.length && t[0] === mark[0] && /^(`+|~+)$/.test(t);
}

function startsBlock(lines: string[], i: number): boolean {
  const line = lines[i]!;
  return (
    FENCE.test(line) ||
    HEADING.test(line) ||
    RULE.test(line) ||
    QUOTE.test(line) ||
    BULLET.test(line) ||
    NUMBERED.test(line) ||
    isTable(lines, i)
  );
}

function plain(lines: string[]): Block[] {
  return [{ t: 'p', c: [{ t: 'text', v: lines.join('\n') }] }];
}

/**
 * The lines of one list: items of its own kind, their indented continuations, and
 * nothing after a blank line followed by anything else. A list of the other kind
 * is a new list, so numbers that follow bullets are not swallowed by the last one.
 */
function takeList(lines: string[], from: number): { lines: string[]; next: number } {
  const ordered = NUMBERED.test(lines[from]!);
  const item = (line: string): boolean => (ordered ? NUMBERED : BULLET).test(line);
  const continued = (line: string): boolean => /^ {2,}\S/.test(line);
  const taken: string[] = [];
  let i = from;
  while (i < lines.length) {
    const line = lines[i]!;
    if (line.trim() === '') {
      // A blank line stays in the list only when another of its items follows.
      const after = lines[i + 1];
      if (after !== undefined && (item(after) || continued(after))) {
        i++;
        continue;
      }
      break;
    }
    if (!item(line) && !continued(line)) break;
    taken.push(line);
    i++;
  }
  return { lines: taken, next: i };
}

function list(lines: string[], depth: number): Block {
  const first = lines[0]!;
  const numbered = NUMBERED.exec(first);
  const ordered = numbered !== null;
  const base = (numbered ?? BULLET.exec(first))![1]!.length;
  const start = numbered ? Number(numbered[2]) : 1;
  const items: string[][] = [];
  for (const line of lines) {
    const marker = (ordered ? NUMBERED : BULLET).exec(line);
    if (marker && marker[1]!.length <= base + 1) {
      items.push([marker[3]!]);
    } else if (items.length > 0) {
      // Deeper lines belong to the item above: a nested list or a continued sentence.
      items[items.length - 1]!.push(line.replace(new RegExp(`^ {0,${base + 2}}`), ''));
    }
  }
  return {
    t: 'list',
    ordered,
    start,
    items: items.map((item) => (depth < MAX_DEPTH ? blocks(item, depth + 1) : plain(item))),
  };
}

function isTable(lines: string[], i: number): boolean {
  const head = lines[i];
  const rule = lines[i + 1];
  if (head === undefined || rule === undefined) return false;
  if (!head.includes('|') || !rule.includes('-') || !TABLE_SEPARATOR.test(rule)) return false;
  return cells(head).length === cells(rule).length && cells(head).length > 0;
}

function cells(line: string): string[] {
  let t = line.trim();
  if (t.startsWith('|')) t = t.slice(1);
  if (t.endsWith('|') && !t.endsWith('\\|')) t = t.slice(0, -1);
  const out: string[] = [];
  let cur = '';
  for (let i = 0; i < t.length; i++) {
    if (t[i] === '\\' && t[i + 1] === '|') {
      cur += '|';
      i++;
    } else if (t[i] === '|') {
      out.push(cur.trim());
      cur = '';
    } else cur += t[i];
  }
  out.push(cur.trim());
  return out;
}

function alignment(cell: string): Align {
  const left = cell.startsWith(':');
  const right = cell.endsWith(':');
  if (left && right) return 'center';
  if (right) return 'right';
  if (left) return 'left';
  return undefined;
}

// ---------------------------------------------------------------------------

/** Only a web address is a link. Anything else, including a scheme in disguise, is not. */
export function safeHref(raw: string): string | undefined {
  const text = raw.trim();
  // Control characters and whitespace inside a URL are how a scheme is hidden.
  for (const ch of text) {
    const n = ch.charCodeAt(0);
    if (n <= 0x20 || (n >= 0x7f && n <= 0x9f)) return undefined;
  }
  try {
    const url = new URL(text);
    return url.protocol === 'http:' || url.protocol === 'https:' ? url.href : undefined;
  } catch {
    return undefined;
  }
}

const SPECIAL = /[`*_[\\\n]|https?:\/\//;

export function inlines(source: string, depth = 0): Inline[] {
  const out: Inline[] = [];
  let text = '';
  const flush = (): void => {
    if (text) out.push({ t: 'text', v: text });
    text = '';
  };
  let i = 0;
  while (i < source.length) {
    const rest = source.slice(i);
    const ch = source[i]!;

    if (ch === '\\' && i + 1 < source.length && /[\\`*_{}[\]()#+\-.!|>~]/.test(source[i + 1]!)) {
      text += source[i + 1];
      i += 2;
      continue;
    }

    if (ch === '\n') {
      flush();
      out.push({ t: 'br' });
      i++;
      continue;
    }

    if (ch === '`') {
      const run = /^`+/.exec(rest)![0];
      const end = source.indexOf(run, i + run.length);
      if (end !== -1) {
        flush();
        out.push({ t: 'code', v: source.slice(i + run.length, end).replace(/^ (.*) $/s, '$1') });
        i = end + run.length;
        continue;
      }
      text += run;
      i += run.length;
      continue;
    }

    if ((ch === '*' || ch === '_') && depth < MAX_DEPTH) {
      const double = source.startsWith(ch + ch, i);
      const mark = double ? ch + ch : ch;
      const close = closer(source, i, mark);
      if (close !== -1) {
        flush();
        const inner = inlines(source.slice(i + mark.length, close), depth + 1);
        out.push(double ? { t: 'strong', c: inner } : { t: 'em', c: inner });
        i = close + mark.length;
        continue;
      }
      text += mark;
      i += mark.length;
      continue;
    }

    if (ch === '[' && depth < MAX_DEPTH) {
      const link = /^\[([^\]\n]*)\]\(\s*<?([^\s)>]+)>?(?:\s+"[^"]*")?\s*\)/.exec(rest);
      const href = link ? safeHref(link[2]!) : undefined;
      if (link && href) {
        flush();
        out.push({ t: 'a', href, c: inlines(link[1]!, depth + 1) });
        i += link[0].length;
        continue;
      }
      text += ch;
      i++;
      continue;
    }

    const bare = /^https?:\/\/[^\s<>()[\]]+/.exec(rest);
    if (bare && (i === 0 || /[\s(]/.test(source[i - 1]!))) {
      // Sentence punctuation after an address is the sentence's, not the address's.
      const url = bare[0].replace(/[.,;:!?'"]+$/, '');
      const href = safeHref(url);
      if (href) {
        flush();
        out.push({ t: 'a', href, c: [{ t: 'text', v: url }] });
        i += url.length;
        continue;
      }
    }

    // Everything up to the next character that can start something is plain text.
    const next = rest.slice(1).search(SPECIAL);
    const run = next === -1 ? rest : rest.slice(0, next + 1);
    text += run;
    i += run.length;
  }
  flush();
  return out;
}

/**
 * Where an emphasis mark opened at `open` closes, or -1. The mark must hug its
 * text (no space just inside), an underscore must not sit inside a word (so
 * `snake_case_name` is a name and not italics), and the first mark that could
 * close it wins.
 */
function closer(source: string, open: number, mark: string): number {
  const ch = mark[0]!;
  const after = source[open + mark.length];
  if (after === undefined || /\s/.test(after)) return -1;
  if (ch === '_' && open > 0 && /\w/.test(source[open - 1]!)) return -1;
  for (let j = open + mark.length; j < source.length; j++) {
    if (source[j] === '`') {
      // Code inside emphasis keeps its own marks.
      const end = source.indexOf('`', j + 1);
      if (end === -1) return -1;
      j = end;
      continue;
    }
    if (source[j] === '\\') {
      j++;
      continue;
    }
    if (!source.startsWith(mark, j)) continue;
    const before = source[j - 1]!;
    if (/\s/.test(before) && j > open + mark.length) continue;
    if (j === open + mark.length) continue;
    if (mark.length === 1 && source[j + 1] === ch) {
      // A single mark does not close on half of a double one.
      j++;
      continue;
    }
    if (ch === '_' && /\w/.test(source[j + mark.length] ?? '')) continue;
    return j;
  }
  return -1;
}

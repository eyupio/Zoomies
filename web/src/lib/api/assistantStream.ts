/**
 * Reading an assistant's answer.
 *
 * The answer is a Server-Sent Events body that arrives in whatever pieces the
 * network cuts it into, so a frame can begin in one chunk and end in the next.
 * The parser holds the unfinished part until the blank line that ends it, and
 * says nothing about a frame whose kind or payload it does not understand: a
 * newer controller may send one, and an older page should keep reading.
 */

export type ChatFrame =
  | { kind: 'delta'; text: string }
  | { kind: 'usage'; inputTokens: number; outputTokens: number }
  | { kind: 'done'; provider: string; model: string }
  | { kind: 'error'; message: string };

export class FrameParser {
  private pending = '';

  /** The frames a chunk completes, in order. */
  push(chunk: string): ChatFrame[] {
    this.pending += chunk.replace(/\r\n/g, '\n');
    const frames: ChatFrame[] = [];
    let end: number;
    while ((end = this.pending.indexOf('\n\n')) >= 0) {
      const block = this.pending.slice(0, end);
      this.pending = this.pending.slice(end + 2);
      const frame = parseBlock(block);
      if (frame) frames.push(frame);
    }
    return frames;
  }
}

function parseBlock(block: string): ChatFrame | undefined {
  let kind = '';
  const data: string[] = [];
  for (const line of block.split('\n')) {
    if (line.startsWith('event:')) kind = line.slice(6).trim();
    else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''));
  }
  if (!kind) return undefined;
  let payload: Record<string, unknown>;
  try {
    const parsed: unknown = JSON.parse(data.join('\n'));
    if (typeof parsed !== 'object' || parsed === null) return undefined;
    payload = parsed as Record<string, unknown>;
  } catch {
    return undefined;
  }
  const text = (key: string) => (typeof payload[key] === 'string' ? (payload[key] as string) : '');
  const count = (key: string) => (typeof payload[key] === 'number' ? (payload[key] as number) : 0);
  switch (kind) {
    case 'delta':
      return { kind, text: text('text') };
    case 'usage':
      return { kind, inputTokens: count('input_tokens'), outputTokens: count('output_tokens') };
    case 'done':
      return { kind, provider: text('provider'), model: text('model') };
    case 'error':
      return { kind, message: text('message') };
    default:
      return undefined;
  }
}

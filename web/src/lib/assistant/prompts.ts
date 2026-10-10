/** Explicit display facts only: callers must never pass credentials or whole API objects. */
export interface EliContext {
  kind: string;
  title: string;
  facts: Record<string, string | number | boolean | null | undefined>;
  question?: string;
}

export interface Suggestion {
  label: string;
  prompt: string;
}

export function contextPrompt(context: EliContext): string {
  const facts = Object.entries(context.facts)
    .filter(([, value]) => value !== undefined && value !== null && value !== '')
    .map(([key, value]) => `${key}: ${value}`)
    .join('\n');
  return `${context.question ?? 'Help me understand this situation, prioritise checks and suggest safe next steps.'}\n\nContext shared from the ${context.kind} UI: ${context.title}\n${facts}\n\nThese facts are a snapshot of what the page showed when I asked, not the fleet now: if you have tools, use them to check the current state before you rely on the snapshot. Ask for missing evidence and distinguish facts from possible causes.`;
}

const OPEN = '<follow-ups>';
const CLOSE = '</follow-ups>';
const MAX_SUGGESTIONS = 3;

/**
 * Split an answer into what the person reads and the next questions the model
 * proposed. The controller asks the model to end every answer with a
 * `<follow-ups>` block, one question per line, so the suggestions are about this
 * conversation and not a fixed list.
 *
 * It is safe to call on a half-written answer: the block, and any prefix of its
 * opening tag at the very end, is withheld so the tag never flashes on screen as
 * it streams. A block that never closes (the model ran out of room) yields no
 * suggestions rather than a cut-off question.
 */
export function splitFollowUps(raw: string): { text: string; suggestions: Suggestion[] } {
  const start = raw.indexOf(OPEN);
  if (start < 0) {
    for (let n = Math.min(OPEN.length - 1, raw.length); n > 0; n--) {
      if (raw.endsWith(OPEN.slice(0, n)))
        return { text: raw.slice(0, -n).trimEnd(), suggestions: [] };
    }
    return { text: raw, suggestions: [] };
  }
  const text = raw.slice(0, start).trimEnd();
  const end = raw.indexOf(CLOSE, start);
  if (end < 0) return { text, suggestions: [] };
  const seen = new Set<string>();
  const suggestions: Suggestion[] = [];
  for (const line of raw.slice(start + OPEN.length, end).split('\n')) {
    const question = line.replace(/^\s*(?:[-*]|\d+[.)])\s*/, '').trim();
    if (!question || question.length > 200 || seen.has(question)) continue;
    seen.add(question);
    suggestions.push({ label: question, prompt: question });
    if (suggestions.length === MAX_SUGGESTIONS) break;
  }
  return { text, suggestions };
}

/**
 * One conversation with Eli, held in memory.
 *
 * The controller keeps nothing between questions: the page sends what was said
 * with each new one, so this is the only place a conversation lives, and a
 * reload ends it. It is a module and not a component's state so that the same
 * conversation can be shown in more than one place and survives moving between
 * pages, which a floating panel needs.
 */
import { ApiError, streamAssistantChat } from '$lib/api/client';
import { supportHint } from '$lib/errors';
import { hiddenNote } from './redaction';
import { splitFollowUps, type EliContext, type Suggestion } from './prompts';

export interface Turn {
  id: number;
  role: 'user' | 'assistant';
  content: string;
  context?: EliContext;
  /** Who answered, once the answer says: the provider and the model. */
  by?: string;
  tokens?: string;
  /** What Eli looked at while answering, in the order it looked. */
  tools?: ToolLook[];
  /** Whether the fleet could be read through the provider that answered. */
  fleetAccess?: boolean;
  /** What was hidden from the model before it answered, in words, or empty. */
  hidden?: string;
  /** Questions the model proposed to ask next, once the answer is complete. */
  suggestions?: Suggestion[];
  error?: string;
  streaming?: boolean;
}

export interface ToolLook {
  name: string;
  status: 'running' | 'done' | 'failed';
}

function failure(cause: unknown): string {
  return cause instanceof ApiError ? cause.message : `That could not be done. ${supportHint()}`;
}

/** Record a look: a new one when it starts, and the end of the last one still running. */
function note(answer: Turn, name: string, status: ToolLook['status']): void {
  const looks = (answer.tools ??= []);
  if (status === 'running') {
    looks.push({ name, status });
    return;
  }
  const open = looks.findLast((l) => l.name === name && l.status === 'running');
  if (open) open.status = status;
  else looks.push({ name, status });
}

export class Conversation {
  turns = $state<Turn[]>([]);
  busy = $state(false);
  draft = $state('');
  #controller: AbortController | undefined;
  #next = 0;
  /** Which provider the last question went to, so that asking again goes to the same one. */
  #provider: string | undefined;

  /** Whether the last answer failed, so the page can offer to ask again. */
  get failed(): boolean {
    const last = this.turns[this.turns.length - 1];
    return last?.role === 'assistant' && !!last.error;
  }

  async send(text: string, context?: EliContext, providerId?: string): Promise<void> {
    const question = text.trim();
    if (!question || this.busy) return;
    this.#provider = providerId ?? this.#provider;
    // What was said before is what the model is told it said: a turn that failed
    // before it began has nothing to repeat, and is left out.
    const history = this.turns
      .filter((t) => t.content && !t.error)
      .map((t) => ({ role: t.role, content: t.content }));
    this.turns.push({ id: this.#next++, role: 'user', content: question, context });
    this.turns.push({ id: this.#next++, role: 'assistant', content: '', streaming: true });
    const answer = this.turns[this.turns.length - 1]!;
    this.busy = true;
    const controller = new AbortController();
    this.#controller = controller;
    // The answer as the model wrote it, follow-up block included; the turn holds only what is read.
    let raw = '';
    try {
      await streamAssistantChat(
        {
          messages: [...history, { role: 'user', content: question }],
          ...(this.#provider ? { provider_id: this.#provider } : {}),
        },
        (frame) => {
          if (frame.kind === 'delta') {
            raw += frame.text;
            answer.content = splitFollowUps(raw).text;
          } else if (frame.kind === 'usage')
            answer.tokens = `${frame.inputTokens} in, ${frame.outputTokens} out`;
          else if (frame.kind === 'tool') note(answer, frame.name, frame.status);
          else if (frame.kind === 'done') {
            answer.suggestions = splitFollowUps(raw).suggestions;
            answer.by = `${frame.provider}, ${frame.model}`;
            answer.fleetAccess = frame.fleetAccess;
            answer.hidden = hiddenNote(frame.redacted.credentials, frame.redacted.emails);
          } else answer.error = frame.message || 'The model stopped answering.';
        },
        controller.signal,
      );
    } catch (cause) {
      // Stop is the person's, not a failure.
      if (!(cause instanceof DOMException && cause.name === 'AbortError'))
        answer.error = failure(cause);
    } finally {
      answer.streaming = false;
      // A conversation cleared mid-answer may already be on its next question.
      if (this.#controller === controller) {
        this.busy = false;
        this.#controller = undefined;
      }
    }
  }

  /** Ask the last question again, replacing the answer that failed. */
  async retry(): Promise<void> {
    if (this.busy || !this.failed) return;
    this.turns.pop();
    const question = this.turns.pop();
    if (question?.role === 'user') await this.send(question.content, question.context);
  }

  stop(): void {
    this.#controller?.abort();
  }

  clear(): void {
    this.#controller?.abort();
    this.#controller = undefined;
    this.busy = false;
    this.turns = [];
    this.draft = '';
  }
}

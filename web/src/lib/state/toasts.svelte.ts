/**
 * The toast queue.
 *
 * Toasts report the outcome of something the operator did. Background events --
 * a runner reaching `removed` on its own -- arrive over SSE and are shown by the
 * page changing, not by a notification.
 *
 * Successes dismiss themselves. Errors do not: an operator who looked away
 * should still find out that the drain was refused.
 */
import { sentence, supportHint } from '$lib/errors';
import { ApiError } from '../api/client';

export type ToastTone = 'info' | 'success' | 'warning' | 'error';

export interface ToastAction {
  label: string;
  run: () => void;
}

export interface Toast {
  id: number;
  tone: ToastTone;
  /** One line, sentence case, no full stop. */
  title: string;
  /** What happened and what to do about it. */
  message?: string;
  action?: ToastAction;
  /** Milliseconds until it dismisses itself; 0 means it stays. */
  timeout: number;
}

export interface ToastInput {
  tone?: ToastTone;
  title: string;
  message?: string;
  action?: ToastAction;
  timeout?: number;
}

const DEFAULT_TIMEOUT = 5000;
/** Beyond this the stack becomes a wall; the oldest self-dismissing one goes. */
const MAX_VISIBLE = 4;
/**
 * The ceiling when every toast on screen is one that stays until it is read.
 * Higher than MAX_VISIBLE, because an unread error is the one thing this queue
 * exists to protect; finite, because a failing loop must not bury the page it
 * is failing on.
 */
const MAX_STICKY = 8;

/**
 * Whether a tone interrupts.
 *
 * A warning is a partial failure -- retention applied to some copies, one
 * remote that refused -- so it belongs with the errors on both counts the two
 * regions exist for. It used to sit with the successes, which meant the one
 * message on the page that said something did not work was announced in the
 * queue that waits for a pause.
 */
function assertive(tone: ToastTone): boolean {
  return tone === 'error' || tone === 'warning';
}

let nextId = 1;

/**
 * Make room for one more.
 *
 * Eviction takes the oldest toast that was going to leave on its own anyway.
 * It used to take the oldest of any kind, so an operator who drained five
 * runners and had one refused watched the four successes push the refusal off
 * the screen before they could read it -- the exact opposite of what
 * "successes dismiss themselves, errors do not" is for.
 */
function evict(items: Toast[]): Toast[] {
  const kept = [...items];
  while (kept.length > MAX_VISIBLE) {
    const oldestTemporary = kept.findIndex((t) => t.timeout > 0);
    if (oldestTemporary === -1) break;
    kept.splice(oldestTemporary, 1);
  }
  return kept.length > MAX_STICKY ? kept.slice(-MAX_STICKY) : kept;
}

class Toasts {
  #items = $state<Toast[]>([]);

  get items(): readonly Toast[] {
    return this.#items;
  }

  /** Everything announced politely: successes and information. */
  get polite(): readonly Toast[] {
    return this.#items.filter((t) => !assertive(t.tone));
  }

  /** Errors and warnings, announced assertively because they interrupt a task. */
  get assertive(): readonly Toast[] {
    return this.#items.filter((t) => assertive(t.tone));
  }

  push(input: ToastInput): number {
    const tone = input.tone ?? 'info';
    const toast: Toast = {
      id: nextId++,
      tone,
      title: input.title,
      message: input.message,
      action: input.action,
      timeout: input.timeout ?? (tone === 'error' ? 0 : DEFAULT_TIMEOUT),
    };
    this.#items = evict([...this.#items, toast]);
    if (toast.timeout > 0) {
      setTimeout(() => this.dismiss(toast.id), toast.timeout);
    }
    return toast.id;
  }

  success(title: string, message?: string): number {
    return this.push({ tone: 'success', title, message });
  }

  info(title: string, message?: string): number {
    return this.push({ tone: 'info', title, message });
  }

  warning(title: string, message?: string): number {
    return this.push({ tone: 'warning', title, message });
  }

  error(title: string, message?: string, action?: ToastAction): number {
    return this.push({ tone: 'error', title, message, action });
  }

  /**
   * Report a failed request. The server's words are shown as they are -- on a
   * 403 they name the role required, and paraphrasing them would lose that --
   * but capitalised and stopped, because the server writes them for a log line
   * and this is a sentence under a sentence-case title. The detail is
   * often a Go error, a path or a URL, so it follows the message untouched.
   */
  fromError(cause: unknown, title: string, action?: ToastAction): number {
    if (cause instanceof ApiError) {
      const message = sentence(cause.message);
      return this.error(title, cause.detail ? `${message} ${cause.detail}` : message, action);
    }
    if (cause instanceof Error) return this.error(title, sentence(cause.message), action);
    return this.error(title, `The cause was not reported. ${supportHint()}`, action);
  }

  dismiss(id: number): void {
    this.#items = this.#items.filter((t) => t.id !== id);
  }

  clear(): void {
    this.#items = [];
  }
}

export const toasts = new Toasts();

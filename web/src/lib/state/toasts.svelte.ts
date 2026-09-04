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
/** Beyond this the stack becomes a wall; the oldest go. */
const MAX_VISIBLE = 4;

let nextId = 1;

class Toasts {
  #items = $state<Toast[]>([]);

  get items(): readonly Toast[] {
    return this.#items;
  }

  /** Everything announced politely: successes and information. */
  get polite(): readonly Toast[] {
    return this.#items.filter((t) => t.tone !== 'error');
  }

  /** Errors and warnings, announced assertively because they interrupt a task. */
  get assertive(): readonly Toast[] {
    return this.#items.filter((t) => t.tone === 'error');
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
    this.#items = [...this.#items, toast].slice(-MAX_VISIBLE);
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
   * Report a failed request. The server's message is shown verbatim -- on a 403
   * it names the role required, and paraphrasing it would lose that.
   */
  fromError(cause: unknown, title: string, action?: ToastAction): number {
    if (cause instanceof ApiError) {
      const detail = cause.detail ? `${cause.message} ${cause.detail}` : cause.message;
      return this.error(title, detail, action);
    }
    if (cause instanceof Error) return this.error(title, cause.message, action);
    return this.error(title, 'The cause was not reported. Check the controller logs.', action);
  }

  dismiss(id: number): void {
    this.#items = this.#items.filter((t) => t.id !== id);
  }

  clear(): void {
    this.#items = [];
  }
}

export const toasts = new Toasts();

/**
 * Eli, as the rest of the page sees Eli: whether there is one to ask, whether the
 * panel is open, and the one conversation.
 *
 * Each signed-in account uses its personal providers. The conversation survives
 * navigation and is forgotten on sign-out or reload.
 */
import { tick } from 'svelte';
import type { EliContext } from './prompts';
import { listAssistantProviders } from '$lib/api/client';
import type { AssistantProvider } from '$lib/api/types';
import { answeringProvider } from '$lib/settings/assistant';
import { session } from '$lib/state/session.svelte';
import { Conversation } from './conversation.svelte';

class Eli {
  readonly conversation = new Conversation();
  providers = $state<readonly AssistantProvider[]>([]);
  open = $state(false);
  pending = $state<EliContext[]>([]);
  returnFocus: HTMLElement | null = null;
  #loaded = $state(false);
  #revision = 0;

  /**
   * The provider that answers this person: the default, if it is enabled and they
   * may use it, and otherwise the first they may. Somebody else's own subscription
   * is never one.
   */
  get answering(): AssistantProvider | undefined {
    return answeringProvider(this.providers);
  }

  /** Whether the widget is shown at all. */
  get available(): boolean {
    return this.#loaded && session.phase === 'ready' && session.can('viewer') && !!this.answering;
  }

  /** Whether Eli can read the fleet through the provider that answers. */
  get fleetAccess(): boolean {
    return this.answering?.fleet_access === true;
  }

  /** Ask the controller which providers there are. Quiet when it cannot say. */
  async refresh(): Promise<void> {
    const revision = ++this.#revision;
    if (session.phase !== 'ready' || !session.can('viewer')) {
      this.providers = [];
      this.open = false;
      return;
    }
    try {
      const result = await listAssistantProviders();
      if (revision !== this.#revision) return;
      this.providers = result.items ?? [];
    } catch {
      // The widget is an extra: a page that cannot say whether there is a model
      // simply does not offer one, and the Settings page says what is wrong.
      if (revision !== this.#revision) return;
      this.providers = [];
    } finally {
      if (revision === this.#revision) this.#loaded = true;
    }
    if (!this.answering) this.open = false;
  }

  /** Use the providers a page has just read, without asking again. */
  know(providers: readonly AssistantProvider[]): void {
    this.#revision++;
    this.providers = providers;
    this.#loaded = true;
    if (!this.answering) this.open = false;
  }

  show(): void {
    if (this.available) this.open = true;
  }

  hide(): void {
    this.close();
  }

  toggle(): void {
    if (this.open) this.hide();
    else this.show();
  }

  ask(context: EliContext): void {
    this.returnFocus =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    this.pending.push(structuredClone($state.snapshot(context)));
    this.open = true;
  }
  close(): void {
    this.open = false;
    const previous = this.returnFocus;
    void tick().then(() =>
      requestAnimationFrame(() => {
        if (this.open) return;
        const target = previous?.isConnected
          ? previous
          : document.querySelector<HTMLButtonElement>('[aria-controls="eli-widget"]');
        target?.focus();
      }),
    );
  }
  newConversation(): void {
    this.conversation.clear();
    this.pending = [];
  }

  /** Forget everything: somebody else may sign in on this tab. */
  reset(): void {
    this.#revision++;
    this.conversation.clear();
    this.providers = [];
    this.pending = [];
    this.open = false;
    this.#loaded = false;
  }
}

export const eli = new Eli();

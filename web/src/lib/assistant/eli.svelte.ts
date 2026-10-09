/**
 * Eli, as the rest of the page sees Eli: whether there is one to ask, whether the
 * panel is open, and the one conversation.
 *
 * Eli is there when an administrator is signed in and an enabled provider is the
 * default; asking the providers is an administrator's call, so anyone else never
 * learns of it and the widget never mounts. The conversation is kept here so it
 * survives moving between pages, and it is gone on a reload, as the controller
 * keeps nothing.
 */
import { listAssistantProviders } from '$lib/api/client';
import type { AssistantProvider } from '$lib/api/types';
import { answeringProvider } from '$lib/settings/assistant';
import { session } from '$lib/state/session.svelte';
import { Conversation } from './conversation.svelte';

class Eli {
  readonly conversation = new Conversation();
  providers = $state<readonly AssistantProvider[]>([]);
  open = $state(false);
  #loaded = $state(false);

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
    return this.#loaded && session.phase === 'ready' && session.can('admin') && !!this.answering;
  }

  /** Whether Eli can read the fleet through the provider that answers. */
  get fleetAccess(): boolean {
    return this.answering?.fleet_access === true;
  }

  /** Ask the controller which providers there are. Quiet when it cannot say. */
  async refresh(): Promise<void> {
    if (session.phase !== 'ready' || !session.can('admin')) {
      this.providers = [];
      this.open = false;
      return;
    }
    try {
      this.providers = (await listAssistantProviders()).items ?? [];
    } catch {
      // The widget is an extra: a page that cannot say whether there is a model
      // simply does not offer one, and the Settings page says what is wrong.
      this.providers = [];
    } finally {
      this.#loaded = true;
    }
    if (!this.answering) this.open = false;
  }

  /** Use the providers a page has just read, without asking again. */
  know(providers: readonly AssistantProvider[]): void {
    this.providers = providers;
    this.#loaded = true;
    if (!this.answering) this.open = false;
  }

  show(): void {
    if (this.available) this.open = true;
  }

  hide(): void {
    this.open = false;
  }

  toggle(): void {
    if (this.open) this.hide();
    else this.show();
  }

  /** Forget everything: somebody else may sign in on this tab. */
  reset(): void {
    this.conversation.clear();
    this.providers = [];
    this.open = false;
    this.#loaded = false;
  }
}

export const eli = new Eli();

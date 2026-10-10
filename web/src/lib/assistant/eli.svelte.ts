import { tick } from 'svelte';
import { Conversation } from './conversation.svelte';
import { type EliContext } from './prompts';

class Eli {
  conversation = new Conversation();
  open = $state(false);
  pending = $state<EliContext[]>([]);
  returnFocus: HTMLElement | null = null;

  ask(context: EliContext): void {
    this.returnFocus =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    this.pending.push(structuredClone($state.snapshot(context)));
    this.open = true;
  }

  close(): void {
    this.open = false;
    const previous = this.returnFocus;
    // The launcher is recreated when the panel closes; focus it after Svelte has rendered it.
    void tick().then(() => {
      if (this.open) return;
      const target = previous?.isConnected
        ? previous
        : document.querySelector<HTMLButtonElement>('[aria-controls="eli-widget"]');
      target?.focus();
    });
  }

  reset(): void {
    this.conversation.clear();
    this.pending = [];
  }
}

export const eli = new Eli();

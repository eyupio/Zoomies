<!--
  Ask the assistant something, with the model the cards above chose.

  This is the first way to use a provider once it is set up: a conversation the
  page holds in memory and sends whole with each question. The controller keeps
  nothing, so closing the page ends it, and nothing in it is stored. The model
  cannot see the fleet yet and is told so; this is where the panel, the tools
  and the stored conversations of the design are built from.

  An answer is rendered as text and nothing else: what a model writes is not
  markup the page should trust.
-->
<script lang="ts">
  import { Send, Square, Trash2 } from '@lucide/svelte';
  import { ApiError, streamAssistantChat } from '$lib/api/client';
  import type { AssistantProvider } from '$lib/api/types';
  import { supportHint } from '$lib/errors';
  import Button from '$lib/components/Button.svelte';
  import Textarea from '$lib/components/Textarea.svelte';

  interface Props {
    providers: readonly AssistantProvider[];
  }
  let { providers }: Props = $props();

  interface Turn {
    id: number;
    role: 'user' | 'assistant';
    content: string;
    /** Who answered, once the answer says. */
    by?: string;
    tokens?: string;
    error?: string;
    streaming?: boolean;
  }

  let turns = $state<Turn[]>([]);
  let draft = $state('');
  let busy = $state(false);
  let controller: AbortController | undefined;
  let next = 0;
  let log = $state<HTMLElement>();

  const answering = $derived(providers.find((p) => p.is_default && p.enabled));

  function failure(cause: unknown): string {
    return cause instanceof ApiError ? cause.message : `That could not be done. ${supportHint()}`;
  }

  function scroll(): void {
    queueMicrotask(() => log?.scrollTo({ top: log.scrollHeight }));
  }

  async function send(): Promise<void> {
    const text = draft.trim();
    if (!text || busy || !answering) return;
    // What was said before is what the model is told it said: a turn that failed
    // before it began has nothing to repeat, and is left out.
    const history = turns
      .filter((t) => t.content && !t.error)
      .map((t) => ({ role: t.role, content: t.content }));
    turns.push({ id: next++, role: 'user', content: text });
    turns.push({ id: next++, role: 'assistant', content: '', streaming: true });
    const answer = turns[turns.length - 1]!;
    draft = '';
    busy = true;
    controller = new AbortController();
    scroll();
    try {
      await streamAssistantChat(
        { messages: [...history, { role: 'user', content: text }] },
        (frame) => {
          if (frame.kind === 'delta') answer.content += frame.text;
          else if (frame.kind === 'usage')
            answer.tokens = `${frame.inputTokens} in, ${frame.outputTokens} out`;
          else if (frame.kind === 'done') answer.by = `${frame.provider}, ${frame.model}`;
          else answer.error = frame.message || 'The model stopped answering.';
          scroll();
        },
        controller.signal,
      );
    } catch (cause) {
      // Stop is the person's, not a failure.
      if (!(cause instanceof DOMException && cause.name === 'AbortError'))
        answer.error = failure(cause);
    } finally {
      answer.streaming = false;
      busy = false;
      controller = undefined;
    }
  }

  function stop(): void {
    controller?.abort();
  }

  function clear(): void {
    controller?.abort();
    turns = [];
  }

  function onkeydown(event: KeyboardEvent): void {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      void send();
    }
  }
</script>

<section class="chat" aria-labelledby="assistant-chat">
  <header>
    <h3 id="assistant-chat">Ask the assistant</h3>
    {#if answering}
      <p class="who">
        Answers from <strong>{answering.name}</strong>, {answering.model}. It cannot see this fleet
        yet, and it cannot change anything.
      </p>
    {:else}
      <p class="who">Add a provider, test it and make it the default to ask it something.</p>
    {/if}
  </header>

  {#if turns.length > 0}
    <div class="log" role="log" aria-live="polite" aria-label="Conversation" bind:this={log}>
      {#each turns as turn (turn.id)}
        <article
          class="turn"
          data-role={turn.role}
          aria-label={turn.role === 'user' ? 'You' : 'Assistant'}
        >
          <h4>{turn.role === 'user' ? 'You' : 'Assistant'}</h4>
          <p class="text">
            {turn.content}{#if turn.streaming && !turn.content}…{/if}
          </p>
          {#if turn.error}<p class="error" role="alert">{turn.error}</p>{/if}
          {#if turn.by || turn.tokens}
            <p class="meta">{[turn.by, turn.tokens].filter(Boolean).join(' · ')}</p>
          {/if}
        </article>
      {/each}
    </div>
  {/if}

  <form
    onsubmit={(event) => {
      event.preventDefault();
      void send();
    }}
  >
    <div class="box" {onkeydown} role="presentation">
      <Textarea
        bind:value={draft}
        rows={3}
        ariaLabel="Message"
        placeholder="Ask about Zoomies, GitHub Actions or running a runner fleet"
        disabled={!answering}
      />
    </div>
    <div class="actions">
      {#if busy}
        <Button icon={Square} onclick={stop}>Stop</Button>
      {:else}
        <Button type="submit" variant="primary" icon={Send} disabled={!answering || !draft.trim()}
          >Send</Button
        >
      {/if}
      {#if turns.length > 0}
        <Button variant="ghost" icon={Trash2} onclick={clear}>New conversation</Button>
      {/if}
    </div>
  </form>
</section>

<style>
  .chat {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    margin-bottom: var(--z-space-6);
    padding-bottom: var(--z-space-6);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  h3,
  h4,
  p {
    margin: 0;
  }
  h3 {
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
  }
  .who {
    margin-top: var(--z-space-1);
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .log {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    max-height: 28rem;
    overflow-y: auto;
  }
  .turn {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .turn[data-role='assistant'] {
    border-left: var(--z-border-width-rail) solid var(--z-accent);
  }
  .turn h4 {
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text-muted);
  }
  .text {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-size: var(--z-text-sm);
  }
  .error {
    font-size: var(--z-text-sm);
    color: var(--z-danger);
  }
  .meta {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
</style>

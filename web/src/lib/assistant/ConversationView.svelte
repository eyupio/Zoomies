<!--
  A conversation with Eli: the messages, an empty state that says what to ask,
  and the box to ask it in.

  Eli's answers are Markdown and are drawn as such; what the person types is
  shown as typed. The view follows a streaming answer only while the person is
  at the bottom: scrolling up to read stops it, and a button offers the way back.
-->
<script lang="ts">
  import { ArrowDown, ArrowUp, RotateCcw, Square } from '@lucide/svelte';
  import { tick } from 'svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import type { Conversation } from './conversation.svelte';
  import EliAvatar from './EliAvatar.svelte';
  import Markdown from './Markdown.svelte';

  interface Props {
    conversation: Conversation;
    /** Whoever is answering. Without one the box is closed. */
    answering?: { name: string; model: string };
    /** What to put in the box's place of a hint when nothing can be asked yet. */
    closedHint?: string;
    /** The height of the whole view. The log scrolls inside it. */
    height?: string;
  }
  let {
    conversation,
    answering,
    closedHint = 'Add a provider, test it and make it the default to ask Eli something.',
    height = 'min(70vh, 40rem)',
  }: Props = $props();

  // Things Eli can answer without seeing the fleet, so the first click is never a refusal.
  const STARTERS = [
    'How do labels decide which pool runs a job?',
    'Why might a job sit queued?',
    'How do ephemeral runners work?',
    'What should I check when a host goes unhealthy?',
  ];

  let draft = $state('');
  let box = $state<HTMLTextAreaElement | null>(null);
  let scroller = $state<HTMLElement>();
  let atBottom = $state(true);

  const last = $derived(conversation.turns[conversation.turns.length - 1]);
  // Changes as an answer grows or finishes (its actions appear), which is what the view follows.
  const growth = $derived(
    [
      conversation.turns.length,
      last?.content.length ?? 0,
      last?.streaming,
      !!last?.error,
      !!last?.by,
    ].join(':'),
  );

  $effect(() => {
    void growth;
    if (atBottom) void tick().then(() => scroller?.scrollTo({ top: scroller.scrollHeight }));
  });

  function onscroll(): void {
    if (!scroller) return;
    atBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 72;
  }

  function jump(): void {
    atBottom = true;
    scroller?.scrollTo({ top: scroller.scrollHeight, behavior: 'smooth' });
  }

  function fit(): void {
    if (!box) return;
    box.style.height = 'auto';
    box.style.height = `${Math.min(box.scrollHeight, 176)}px`;
  }

  async function ask(text: string): Promise<void> {
    const question = text.trim();
    if (!question || conversation.busy || !answering) return;
    draft = '';
    atBottom = true;
    void tick().then(fit);
    await conversation.send(question);
  }

  function onkeydown(event: KeyboardEvent): void {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      void ask(draft);
    }
  }
</script>

<div class="view" style:height>
  <div class="stage">
    <div
      class="log"
      role="log"
      aria-live="polite"
      aria-label="Conversation"
      bind:this={scroller}
      {onscroll}
    >
      {#if conversation.turns.length === 0}
        <div class="hello">
          <EliAvatar size={48} />
          <p class="title">Hi, I'm Eli</p>
          <p class="sub">
            Ask me about Zoomies, GitHub Actions or running a runner fleet. I cannot see this fleet
            yet, so for anything about yours, paste what I need.
          </p>
          {#if answering}
            <ul class="starters" aria-label="Things to ask">
              {#each STARTERS as starter (starter)}
                <li>
                  <button type="button" class="starter" onclick={() => void ask(starter)}
                    >{starter}</button
                  >
                </li>
              {/each}
            </ul>
          {/if}
        </div>
      {/if}

      {#each conversation.turns as turn (turn.id)}
        {#if turn.role === 'user'}
          <article class="me" aria-label="You">
            <p>{turn.content}</p>
          </article>
        {:else}
          <article class="eli" aria-label="Eli" aria-busy={turn.streaming ? 'true' : undefined}>
            <EliAvatar />
            <div class="body">
              <p class="name">Eli</p>
              {#if turn.content}
                <Markdown source={turn.content} />
              {:else if turn.streaming}
                <p class="thinking">
                  <span class="dots" aria-hidden="true"><i></i><i></i><i></i></span>
                  <span class="sr-only">Eli is thinking</span>
                </p>
              {/if}
              {#if turn.error}
                <p class="error" role="alert">{turn.error}</p>
              {/if}
              {#if !turn.streaming && (turn.content || turn.error)}
                <div class="actions">
                  {#if turn.content}
                    <CopyButton value={turn.content} label="Copy answer" />
                  {/if}
                  {#if turn.error && turn === last}
                    <button type="button" class="retry" onclick={() => void conversation.retry()}>
                      <RotateCcw size={14} aria-hidden="true" /> Try again
                    </button>
                  {/if}
                  {#if turn.by || turn.tokens}
                    <span class="meta">{[turn.by, turn.tokens].filter(Boolean).join(' · ')}</span>
                  {/if}
                </div>
              {/if}
            </div>
          </article>
        {/if}
      {/each}
    </div>

    {#if !atBottom && conversation.turns.length > 0}
      <button type="button" class="latest" onclick={jump}>
        <ArrowDown size={14} aria-hidden="true" /> Latest
      </button>
    {/if}
  </div>

  <form
    class="composer"
    onsubmit={(event) => {
      event.preventDefault();
      void ask(draft);
    }}
  >
    <div class="field" data-closed={answering ? undefined : ''}>
      <textarea
        bind:this={box}
        bind:value={draft}
        rows="1"
        aria-label="Message"
        placeholder={answering ? 'Ask Eli anything about Zoomies or GitHub Actions' : closedHint}
        disabled={!answering}
        oninput={fit}
        {onkeydown}></textarea>
      {#if conversation.busy}
        <button
          type="button"
          class="send stop"
          aria-label="Stop"
          onclick={() => conversation.stop()}
        >
          <Square size={14} aria-hidden="true" />
        </button>
      {:else}
        <button type="submit" class="send" aria-label="Send" disabled={!answering || !draft.trim()}>
          <ArrowUp size={16} aria-hidden="true" />
        </button>
      {/if}
    </div>
    {#if answering}
      <p class="hint">
        Enter to send, Shift and Enter for a new line. Eli can be wrong: check anything you act on.
      </p>
    {/if}
  </form>
</div>

<style>
  .view {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    min-height: 0;
  }
  .stage {
    position: relative;
    display: flex;
    flex: 1;
    min-height: 0;
  }
  .log {
    display: flex;
    flex: 1;
    flex-direction: column;
    gap: var(--z-space-5);
    min-width: 0;
    padding: var(--z-space-4);
    overflow-y: auto;
    overscroll-behavior: contain;
    background: var(--z-surface);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-lg);
  }
  p {
    margin: 0;
  }

  .hello {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--z-space-2);
    margin: auto;
    max-width: 34rem;
    padding: var(--z-space-4) 0;
    text-align: center;
  }
  .title {
    margin-top: var(--z-space-1);
    font-size: var(--z-text-lg);
    font-weight: var(--z-weight-semibold);
  }
  .sub {
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .starters {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
    gap: var(--z-space-2);
    width: 100%;
    margin: var(--z-space-3) 0 0;
    padding: 0;
    list-style: none;
  }
  .starter {
    width: 100%;
    height: 100%;
    padding: var(--z-space-2) var(--z-space-3);
    font: inherit;
    font-size: var(--z-text-sm);
    text-align: left;
    color: var(--z-text);
    background: var(--z-surface);
    border: var(--z-border-width) solid var(--z-border-strong);
    border-radius: var(--z-radius-md);
    cursor: pointer;
    transition: background var(--z-motion-fast) var(--z-ease);
  }
  .starter:hover {
    background: var(--z-accent-subtle);
    border-color: var(--z-accent-border);
  }

  .me {
    align-self: flex-end;
    max-width: min(85%, 36rem);
    padding: var(--z-space-2) var(--z-space-3);
    background: var(--z-accent-subtle);
    border: var(--z-border-width) solid var(--z-accent-border);
    border-radius: var(--z-radius-lg) var(--z-radius-lg) var(--z-radius-sm) var(--z-radius-lg);
  }
  .me p {
    font-size: var(--z-text-sm);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  .eli {
    display: flex;
    gap: var(--z-space-3);
    min-width: 0;
  }
  .body {
    display: flex;
    flex: 1;
    flex-direction: column;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .name {
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text-muted);
  }
  .error {
    font-size: var(--z-text-sm);
    color: var(--z-danger);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .retry {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    padding: var(--z-space-1) var(--z-space-2);
    font: inherit;
    color: var(--z-text);
    background: var(--z-surface);
    border: var(--z-border-width) solid var(--z-border-strong);
    border-radius: var(--z-radius-sm);
    cursor: pointer;
  }
  .meta {
    margin-left: auto;
    color: var(--z-text-subtle);
  }

  .thinking {
    display: flex;
    align-items: center;
    height: var(--z-leading-base);
  }
  .dots {
    display: inline-flex;
    gap: var(--z-space-1);
  }
  .dots i {
    width: 6px;
    height: 6px;
    background: var(--z-text-subtle);
    border-radius: var(--z-radius-full);
    animation: bounce 1.1s var(--z-ease) infinite;
  }
  .dots i:nth-child(2) {
    animation-delay: 0.15s;
  }
  .dots i:nth-child(3) {
    animation-delay: 0.3s;
  }
  @keyframes bounce {
    0%,
    60%,
    100% {
      opacity: 0.35;
      transform: translateY(0);
    }
    30% {
      opacity: 1;
      transform: translateY(-3px);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .dots i {
      animation: none;
    }
  }

  .latest {
    position: absolute;
    bottom: var(--z-space-3);
    left: 50%;
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    padding: var(--z-space-1) var(--z-space-3);
    font: inherit;
    font-size: var(--z-text-xs);
    color: var(--z-text);
    background: var(--z-surface-raised);
    border: var(--z-border-width) solid var(--z-border-strong);
    border-radius: var(--z-radius-full);
    box-shadow: var(--z-shadow-md);
    transform: translateX(-50%);
    cursor: pointer;
  }

  .composer {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
  }
  .field {
    display: flex;
    align-items: flex-end;
    gap: var(--z-space-2);
    padding: var(--z-space-2) var(--z-space-2) var(--z-space-2) var(--z-space-3);
    background: var(--z-surface);
    border: var(--z-border-width) solid var(--z-border-strong);
    border-radius: var(--z-radius-lg);
  }
  .field:focus-within {
    border-color: var(--z-accent);
    outline: var(--z-focus-width) solid var(--z-focus-colour);
    outline-offset: var(--z-focus-gap);
  }
  .field[data-closed] {
    background: var(--z-surface-sunken);
  }
  textarea {
    flex: 1;
    min-width: 0;
    max-height: 11rem;
    padding: var(--z-space-1) 0;
    font-family: var(--z-font-sans);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-base);
    color: var(--z-text);
    background: none;
    border: 0;
    outline: 0;
    resize: none;
  }
  textarea::placeholder {
    color: var(--z-text-subtle);
  }
  textarea:disabled {
    cursor: not-allowed;
  }
  .send {
    display: inline-flex;
    flex: none;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
    color: var(--z-accent-contrast);
    background: var(--z-accent);
    border: 0;
    border-radius: var(--z-radius-full);
    cursor: pointer;
  }
  .send:hover:not(:disabled) {
    background: var(--z-accent-hover);
  }
  .send:disabled {
    color: var(--z-text-subtle);
    background: var(--z-surface-sunken);
    cursor: not-allowed;
  }
  .hint {
    padding: 0 var(--z-space-2);
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  @media (hover: none) {
    .hint {
      display: none;
    }
  }
</style>

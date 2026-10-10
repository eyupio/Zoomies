<!--
  A conversation with Eli: the messages, an empty state that says what to ask,
  and the box to ask it in.

  Eli's answers are Markdown and are drawn as such; what the person types is
  shown as typed. The view follows a streaming answer only while the person is
  at the bottom: scrolling up to read stops it, and a button offers the way back.
-->
<script lang="ts">
  import {
    ArrowDown,
    ArrowUp,
    Check,
    LoaderCircle,
    RotateCcw,
    Square,
    TriangleAlert,
  } from '@lucide/svelte';
  import { tick } from 'svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import type { Conversation } from './conversation.svelte';
  import EliAvatar from './EliAvatar.svelte';
  import Markdown from './Markdown.svelte';
  import { followUps } from './prompts';
  import { toolLabel } from './tools';

  interface Props {
    conversation: Conversation;
    /** Whoever is answering. Without one the box is closed. */
    answering?: { id: string; name: string; model: string };
    /** What to put in the box's place of a hint when nothing can be asked yet. */
    closedHint?: string;
    /** Whether Eli can read the fleet through whoever is answering. */
    fleetAccess?: boolean;
    /** The height of the whole view. The log scrolls inside it. */
    height?: string;
  }
  let {
    conversation,
    answering,
    closedHint = 'Add a provider, test it and make it the default to ask Eli something.',
    fleetAccess = false,
    height = 'min(70vh, 40rem)',
  }: Props = $props();

  // What to offer first. Without the fleet these are things Eli can answer
  // without seeing it, so the first click is never a refusal; with it they are
  // questions only looking can answer.
  const GENERAL = [
    'How do labels decide which pool runs a job?',
    'Why might a job sit queued?',
    'How do ephemeral runners work?',
    'What should I check when a host goes unhealthy?',
  ];
  const ABOUT_THE_FLEET = [
    'How is the fleet doing right now?',
    'Is anything wrong that I should look at?',
    'Which jobs are queued, and why?',
    'How busy are the pools?',
  ];
  const starters = $derived(fleetAccess ? ABOUT_THE_FLEET : GENERAL);

  let box = $state<HTMLTextAreaElement | null>(null);
  let scroller = $state<HTMLElement>();
  let atBottom = $state(true);

  const suggestions = $derived(followUps(conversation.turns));
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
    // An empty conversation is a greeting to read from its top, not a log to follow.
    if (atBottom && conversation.turns.length > 0)
      void tick().then(() => scroller?.scrollTo({ top: scroller.scrollHeight }));
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
    conversation.draft = '';
    atBottom = true;
    void tick().then(fit);
    await conversation.send(question, undefined, answering.id);
  }

  function onkeydown(event: KeyboardEvent): void {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      void ask(conversation.draft);
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
            {#if fleetAccess}
              Ask me about Zoomies, GitHub Actions or this fleet. I can look at its runners, jobs,
              pools and hosts, and I only read in this chat.
            {:else}
              Ask me about Zoomies, GitHub Actions or running a runner fleet. I cannot see this
              fleet yet, so for anything about yours, use Ask Eli to share displayed details or
              paste what I need.
            {/if}
            Request code changes through PR repairs in Assistant settings.
          </p>
          {#if answering}
            <ul class="starters" aria-label="Things to ask">
              {#each starters as starter (starter)}
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
            <p>{turn.context ? turn.content.split('\n\nContext shared from')[0] : turn.content}</p>
            {#if turn.context}
              <details class="attachment">
                <summary>Shared {turn.context.kind}: {turn.context.title}</summary>
                <dl>
                  {#each Object.entries(turn.context.facts).filter(([, value]) => value !== undefined && value !== null && value !== '') as [key, value] (key)}
                    <div>
                      <dt>{key}</dt>
                      <dd>{value}</dd>
                    </div>
                  {/each}
                </dl>
                <p class="name">Snapshot shared when you asked.</p>
              </details>
            {/if}
          </article>
        {:else}
          <article class="eli" aria-label="Eli" aria-busy={turn.streaming ? 'true' : undefined}>
            <EliAvatar />
            <div class="body">
              <p class="name">Eli</p>
              {#if turn.tools && turn.tools.length > 0}
                <ul class="looks" aria-label="What Eli looked at">
                  {#each turn.tools as look, k (k)}
                    <li data-status={look.status}>
                      {#if look.status === 'running'}
                        <LoaderCircle size={12} class="spin" aria-hidden="true" />
                      {:else if look.status === 'failed'}
                        <TriangleAlert size={12} aria-hidden="true" />
                      {:else}
                        <Check size={12} aria-hidden="true" />
                      {/if}
                      {look.status === 'running'
                        ? 'Looking at'
                        : look.status === 'failed'
                          ? 'Could not look at'
                          : 'Looked at'}
                      {toolLabel(look.name)}
                    </li>
                  {/each}
                </ul>
              {/if}
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
      {#if answering && !conversation.busy && suggestions.length > 0}
        <div class="next">
          <p class="name">Continue the conversation</p>
          <ul class="starters" aria-label="Follow-up questions">
            {#each suggestions as suggestion (suggestion.prompt)}
              <li>
                <button type="button" class="starter" onclick={() => void ask(suggestion.prompt)}
                  >{suggestion.label}<ArrowUp size={13} aria-hidden="true" /></button
                >
              </li>
            {/each}
          </ul>
        </div>
      {/if}
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
      void ask(conversation.draft);
    }}
  >
    <div class="field" data-closed={answering ? undefined : ''}>
      <textarea
        bind:this={box}
        bind:value={conversation.draft}
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
        <button
          type="submit"
          class="send"
          aria-label="Send"
          disabled={!answering || !conversation.draft.trim()}
        >
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
  .next .starters {
    grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
    margin-top: var(--z-space-2);
  }
  .next .starter {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-2);
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

  .attachment {
    margin-top: var(--z-space-2);
    font-size: var(--z-text-xs);
  }
  .attachment summary {
    cursor: pointer;
    color: var(--z-accent);
    overflow-wrap: anywhere;
  }
  .attachment dl {
    margin-block: var(--z-space-2);
  }
  .attachment dl div {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin-block: var(--z-space-1);
  }
  .attachment dt {
    font-weight: var(--z-weight-semibold);
  }
  .attachment dd {
    margin: 0;
    overflow-wrap: anywhere;
    white-space: pre-wrap;
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

  .looks {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .looks li {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    padding: var(--z-nudge-1) var(--z-space-2);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    background: var(--z-surface-sunken);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-full);
  }
  .looks li[data-status='failed'] {
    color: var(--z-danger);
    border-color: var(--z-danger-border);
  }
  .looks :global(.spin) {
    animation: turn 1s linear infinite;
  }
  @keyframes turn {
    to {
      transform: rotate(360deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .looks :global(.spin) {
      animation: none;
    }
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

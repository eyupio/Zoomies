<!--
  Waiting for the controller to come back.

  Applying a staged restore stops the process, and what happens next is up to
  whatever started it: a service manager brings it back in a few seconds, and a
  controller somebody ran in a terminal stays down until they start it again.
  This watches the health probe through both halves -- gone, then back -- and
  says which half it is in, because a page that only spins gives an operator no
  way to tell "still restarting" from "never coming back". When it does come
  back the page reloads: the restore ended every session, so what loads is the
  sign-in page, and behind it a fenced fleet.

  An update restarts the controller too, and borrows the rest of this. What
  differs is handed in: the words, the page to return to, how long the start is
  given, and what counts as the controller answering. An update cannot use the
  health probe, because a restart quicker than its interval falls between two
  probes and is never seen; it passes the event stream's state instead, and
  begins in the second half, since it mounts this only once the stream has gone.
-->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { Power, RefreshCw, TriangleAlert } from '@lucide/svelte';
  import Button from '$lib/components/Button.svelte';
  import { RESTORE_RESTART, type RestartCopy, type RestartPhase } from './restart-copy';

  /*
    How long each half is given before the page stops promising anything. A
    service manager restarts in seconds; a container runtime in a few more; a
    process that has taken longer than this was not restarted by anything.
  */
  const STOP_LIMIT_S = 45;
  const START_LIMIT_S = 120;
  const TICK_MS = 1000;

  async function alive(): Promise<boolean> {
    try {
      const res = await fetch('/healthz', { cache: 'no-store', credentials: 'same-origin' });
      return res.ok;
    } catch {
      return false;
    }
  }

  interface Props {
    /** What the restart is for, in a phrase: "restoring zoomies-…". */
    reason: string;
    /** The words for each half. A restore's, unless the caller has its own. */
    copy?: RestartCopy;
    /** Where the page goes once the controller answers, and when the reader asks to reload. */
    returnTo?: string;
    /** `starting` when the caller has already seen the controller go. */
    begin?: Extract<RestartPhase, 'stopping' | 'starting'>;
    /** Seconds the start is given before the page stops promising anything. */
    startLimit?: number;
    /** Whether the controller is answering. The health probe, unless the caller knows better. */
    answering?: () => boolean | Promise<boolean>;
  }

  let {
    reason,
    copy = RESTORE_RESTART,
    returnTo = '/settings/backups',
    begin = 'stopping',
    startLimit = START_LIMIT_S,
    answering = alive,
  }: Props = $props();

  // Read once: which half it begins in is where it began, and the phase is the
  // component's own from then on.
  // svelte-ignore state_referenced_locally
  let phase = $state<RestartPhase>(begin);
  let elapsed = $state(0);

  let timer: ReturnType<typeof setInterval> | null = null;

  async function tick(): Promise<void> {
    elapsed += 1;
    const up = await answering();
    if (phase === 'stopping') {
      if (!up) {
        phase = 'starting';
        elapsed = 0;
      } else if (elapsed >= STOP_LIMIT_S) {
        phase = 'stuck-up';
        stop();
      }
      return;
    }
    if (phase === 'starting') {
      if (up) {
        phase = 'back';
        stop();
        // A moment for the reader to see it, then the page starts over.
        setTimeout(() => window.location.replace(returnTo), 900);
      } else if (elapsed >= startLimit) {
        phase = 'stuck-down';
        stop();
      }
    }
  }

  function stop(): void {
    if (timer) clearInterval(timer);
    timer = null;
  }

  $effect(() => {
    timer = setInterval(() => void tick(), TICK_MS);
    return stop;
  });
  onDestroy(stop);

  const title = $derived(copy.titles[phase]);
</script>

<section
  class="wait"
  data-phase={phase}
  aria-live="polite"
  aria-busy={phase === 'stopping' || phase === 'starting'}
>
  <div class="glyph" aria-hidden="true">
    {#if phase === 'stuck-up' || phase === 'stuck-down'}
      <TriangleAlert size={22} />
    {:else if phase === 'back'}
      <Power size={22} />
    {:else}
      <span class="ring"></span>
    {/if}
  </div>
  <div class="text">
    <h3>{title}</h3>
    <p class="reason">{reason}</p>
    {#if phase === 'stopping'}
      <p>{copy.stopping}</p>
    {:else if phase === 'starting'}
      <p>{copy.starting}</p>
    {:else if phase === 'back'}
      <p>{copy.back}</p>
    {:else if phase === 'stuck-up'}
      <p>{copy.stuckUp(STOP_LIMIT_S)}</p>
    {:else}
      <p>{copy.stuckDown(startLimit)}</p>
      {#if copy.command}<pre class="mono">{copy.command}</pre>{/if}
    {/if}
    {#if phase === 'stuck-up' || phase === 'stuck-down'}
      <div class="actions">
        <Button
          size="sm"
          variant="secondary"
          icon={RefreshCw}
          onclick={() => window.location.replace(returnTo)}
        >
          Reload the page
        </Button>
      </div>
    {/if}
  </div>
</section>

<style>
  .wait {
    display: flex;
    gap: var(--z-space-4);
    align-items: flex-start;
    padding: var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
    background: var(--z-draining-subtle);
    box-shadow: inset var(--z-nudge-1) 0 0 0 var(--z-draining);
  }
  .wait[data-phase='back'] {
    background: var(--z-idle-subtle);
    box-shadow: inset var(--z-nudge-1) 0 0 0 var(--z-idle);
  }
  .wait[data-phase='stuck-up'],
  .wait[data-phase='stuck-down'] {
    background: var(--z-danger-subtle);
    box-shadow: inset var(--z-nudge-1) 0 0 0 var(--z-danger);
  }
  .glyph {
    display: flex;
    align-items: center;
    justify-content: center;
    flex: none;
    width: var(--z-space-10);
    height: var(--z-space-10);
    border-radius: var(--z-radius-full);
    background: var(--z-surface);
    color: var(--z-text-muted);
  }
  .ring {
    width: var(--z-space-5);
    height: var(--z-space-5);
    border: var(--z-border-width-thick) solid currentColor;
    border-top-color: transparent;
    border-radius: var(--z-radius-full);
    animation: spin calc(var(--z-motion-slow) * 2) linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .ring {
      animation: none;
      border-top-color: currentColor;
      opacity: 0.5;
    }
  }
  .text {
    min-width: 0;
  }
  h3 {
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .reason {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  p {
    margin: var(--z-space-2) 0 0;
    max-width: var(--z-measure-prose);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  pre {
    margin: var(--z-space-2) 0 0;
    padding: var(--z-space-2) var(--z-space-3);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-xs);
    color: var(--z-text);
  }
  .actions {
    margin-top: var(--z-space-3);
  }
</style>

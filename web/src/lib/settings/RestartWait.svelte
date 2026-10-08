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
-->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { Power, RefreshCw, TriangleAlert } from '@lucide/svelte';
  import Button from '$lib/components/Button.svelte';

  interface Props {
    /** What the restart is for, in a phrase: "restoring zoomies-…". */
    reason: string;
  }

  let { reason }: Props = $props();

  type Phase = 'stopping' | 'starting' | 'back' | 'stuck-up' | 'stuck-down';
  let phase = $state<Phase>('stopping');
  let elapsed = $state(0);

  /*
    How long each half is given before the page stops promising anything. A
    service manager restarts in seconds; a container runtime in a few more; a
    process that has taken longer than this was not restarted by anything.
  */
  const STOP_LIMIT_S = 45;
  const START_LIMIT_S = 120;
  const TICK_MS = 1000;

  let timer: ReturnType<typeof setInterval> | null = null;

  async function alive(): Promise<boolean> {
    try {
      const res = await fetch('/healthz', { cache: 'no-store', credentials: 'same-origin' });
      return res.ok;
    } catch {
      return false;
    }
  }

  async function tick(): Promise<void> {
    elapsed += 1;
    const up = await alive();
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
        setTimeout(() => window.location.replace('/settings/backups'), 900);
      } else if (elapsed >= START_LIMIT_S) {
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

  const title = $derived(
    {
      stopping: 'Stopping the controller',
      starting: 'Waiting for it to start again',
      back: 'It is back',
      'stuck-up': 'The controller has not stopped',
      'stuck-down': 'The controller has not come back',
    }[phase],
  );
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
      <p>
        The restore is applied by the next controller to start, before it opens the database. This
        page is watching for the process to stop.
      </p>
    {:else if phase === 'starting'}
      <p>
        It has stopped. A service manager starts it again in a few seconds; this page reloads the
        moment it answers. Everyone is signed out by the restore, so what loads is the sign-in page,
        and behind it a fleet held for recovery until you lift the fence.
      </p>
    {:else if phase === 'back'}
      <p>Reloading.</p>
    {:else if phase === 'stuck-up'}
      <p>
        {STOP_LIMIT_S} seconds on, the controller still answers. It was asked to stop and did not, which
        usually means a long-running request held it open. The restore stays staged: when the process
        does stop, the next start applies it.
      </p>
    {:else}
      <p>
        It stopped and nothing has started it in {START_LIMIT_S} seconds. If it runs under systemd or
        a container with a restart policy, look at that; if you ran it by hand, start it again — the staged
        restore is applied when it starts, whoever starts it.
      </p>
      <pre class="mono">zoomies controller</pre>
    {/if}
    {#if phase === 'stuck-up' || phase === 'stuck-down'}
      <div class="actions">
        <Button
          size="sm"
          variant="secondary"
          icon={RefreshCw}
          onclick={() => window.location.replace('/settings/backups')}
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

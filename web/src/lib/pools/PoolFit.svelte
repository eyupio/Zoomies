<!--
  What the fleet makes of this pool, in the controller's own words.

  The hosts section counts hosts by the host selector, because that is the
  only rule it is editing and the only one it can answer on its own. The
  controller counts them by the whole placement rule -- selector, backend,
  platform, and whether one runner of this pool would fit on the machine at all
  -- so its number can be smaller, and used to become smaller silently: "every
  connected host matches (2 hosts)" in one section and "1 connected host can run
  this pool" in the next, with nothing on screen to say which host went or
  why. This is that missing sentence, and it is shown wherever a setting that
  can cause it is being edited rather than only at the end.
-->
<script lang="ts">
  import { CircleCheck, Info, ServerCog, ServerOff, TriangleAlert, Wrench } from '@lucide/svelte';
  import type { Problem, Result } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import RemedyText from '$lib/components/RemedyText.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  interface Props {
    verdict: Result<'validatePool'> | null;
    /** True while a verdict is on its way, so a stale count can say so. */
    validating?: boolean;
    class?: string;
  }

  let { verdict, validating = false, class: className = '' }: Props = $props();

  const matching = $derived(verdict?.matching_hosts);
  const selected = $derived(verdict?.selected_hosts ?? 0);
  const excluded = $derived(verdict?.excluded_hosts ?? []);
  // Hosts that run the pool at its minimum are counted as running it, because
  // they do: the minimum is the operator saying what they will accept. They
  // are listed as information, in the neutral colours, so the smaller runners
  // there are not a surprise -- never in the warning's amber.
  const reduced = $derived(verdict?.reduced_hosts ?? []);
  // The server says the same thing as the banner, only with the detail the
  // fleet knows. It is shown inside the banner rather than a second time under
  // it, and only when the per-host list below is empty -- with hosts to name,
  // the sentence would be the same facts twice.
  const noHost = $derived<Problem | undefined>(
    verdict?.warnings?.find((w) => w.code === 'pool.no_matching_hosts'),
  );
  // A host held back by an operator is not this pool's doing, and colouring it
  // as a warning would put an amber box on the page for as long as anything
  // in the fleet is cordoned. The pool's own settings turning a host down is
  // the case worth catching the eye, because it is the one a different answer
  // on this screen would fix.
  const poolsDoing = $derived(excluded.some((host) => host.code !== 'unavailable'));
</script>

{#if verdict === null && validating}
  <div class="fit checking {className}" aria-busy="true">
    <Skeleton width="55%" height="0.9rem" />
  </div>
{:else if matching !== undefined}
  <div
    class="fit {className}"
    class:none={matching === 0}
    class:partial={matching > 0 && poolsDoing}
    class:aside={matching > 0 && excluded.length > 0 && !poolsDoing}
    class:stale={validating}
    aria-live="polite"
  >
    {#if matching === 0}
      <p class="title">
        <ServerOff size={15} aria-hidden="true" />
        No connected host can run this pool
      </p>
      <p class="body">
        It would never make a runner, and every job asking for its labels would sit in the queue.
      </p>
    {:else if excluded.length > 0}
      <p class="title">
        {#if poolsDoing}
          <TriangleAlert size={15} aria-hidden="true" />
        {:else}
          <ServerCog size={15} aria-hidden="true" />
        {/if}
        {matching} of the {pluralise(selected, 'host')} this pool reaches can run it
      </p>
      {#if poolsDoing}
        <p class="body">
          Matching the host selector is not the whole of it: a host also has to offer the backend,
          be the machine the pool asked for, and be big enough for one of its runners.
        </p>
      {/if}
    {:else}
      <p class="title">
        <CircleCheck size={15} aria-hidden="true" />
        {pluralise(matching, 'connected host')} can run this pool
      </p>
    {/if}

    {#if excluded.length > 0}
      <ul class="excluded">
        {#each excluded as host (host.host + host.code)}
          <li>
            <span class="host">{host.host}</span>
            <span class="reason">{host.reason}</span>
          </li>
        {/each}
      </ul>
    {:else if matching === 0 && noHost?.detail}
      <p class="body"><RemedyText text={noHost.detail} /></p>
    {/if}

    {#if matching > 0 && reduced.length > 0}
      <div class="reduced">
        <p class="note">
          <Info size={13} aria-hidden="true" />
          {pluralise(reduced.length, 'host')} will run it smaller than it runs on a larger machine
        </p>
        <ul class="excluded">
          {#each reduced as host (host.host)}
            <li>
              <span class="host">{host.host}</span>
              <span class="reason">{host.reason}</span>
            </li>
          {/each}
        </ul>
      </div>
    {/if}

    {#if matching === 0 && noHost?.fix}
      <p class="fix">
        <Wrench size={13} aria-hidden="true" />
        <span><RemedyText text={noHost.fix} /></span>
      </p>
    {/if}
  </div>
{/if}

<style>
  .fit {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-idle-border);
    border-radius: var(--z-radius-md);
    background: var(--z-idle-subtle);
  }
  .fit.partial {
    border-color: var(--z-pending-border);
    background: var(--z-pending-subtle);
  }
  .fit.aside {
    border-color: var(--z-border);
    background: var(--z-surface-sunken);
  }
  .fit.aside .title {
    color: var(--z-text);
  }
  .fit.none {
    border: var(--z-border-width-thick) solid var(--z-danger-border);
    background: var(--z-danger-subtle);
  }
  /* A count being refreshed is still the last true answer, so it fades rather
     than disappearing: replacing it with a skeleton on every keystroke made
     the block flicker under the field that was being typed in. */
  .fit.stale {
    opacity: 0.6;
  }
  .checking {
    border-color: var(--z-border);
    background: var(--z-surface-sunken);
  }
  .title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-idle);
  }
  .fit.partial .title {
    color: var(--z-pending);
  }
  .fit.none .title {
    color: var(--z-danger);
  }
  .body {
    margin: 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .excluded {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .excluded li {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--z-space-2);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .host {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-2xs);
    color: var(--z-text);
  }
  .reason {
    flex: 1 1 20ch;
    color: var(--z-text-muted);
  }
  .reduced {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    padding: var(--z-space-2) var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .note {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .fix {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
</style>

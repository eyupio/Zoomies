<!--
  What the controller changed on its own, and a way back.

  With "Apply suggested changes automatically" on, the controller makes the
  change a problem proposes once it has stood for two hours. A fleet that edits
  itself owes whoever reads it a list of what it did, and a button that undoes
  it, so this sits at the foot of the problems drawer: each change with what it
  was, when, and an Undo while it can be undone. An Undo is refused once the pool
  or host has been edited since, because putting the old value back would undo
  that edit too, and it is not offered then.
-->
<script lang="ts">
  import { Undo2 } from '@lucide/svelte';
  import { listAutoApplied, undoAutoApplied } from '$lib/api/client';
  import type { AutoAppliedChange, ShadowedChange } from '$lib/api/types';
  import { pluralise, relativeTime } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';

  let items = $state<AutoAppliedChange[]>([]);
  let enabled = $state(false);
  let wouldApply = $state<ShadowedChange[]>([]);
  let busy = $state<string | null>(null);

  async function load(): Promise<void> {
    try {
      const answer = await listAutoApplied();
      items = answer.items;
      enabled = answer.enabled;
      wouldApply = answer.would_apply;
    } catch {
      // The list is a convenience under the problems: if it cannot be read the
      // problems above it are still the page.
      items = [];
      wouldApply = [];
    }
  }

  async function undo(change: AutoAppliedChange): Promise<void> {
    busy = change.id;
    try {
      await undoAutoApplied(change.id);
      toasts.success('Change undone', change.label);
      void fleet.reconcile();
      await load();
    } catch (cause) {
      toasts.fromError(cause, 'That change was not undone');
    } finally {
      busy = null;
    }
  }

  $effect(() => {
    void load();
  });
</script>

{#if wouldApply.length > 0 && !enabled}
  <section class="auto" aria-label="Changes that would be made automatically">
    <h3 class="title">Would be made automatically</h3>
    <p class="note">
      Automatic apply is in shadow mode, so nothing below has been changed. These are the changes it
      would have made, after the proposal stood for two hours. Turn it on in Settings to make them.
    </p>
    <ul class="items">
      {#each wouldApply as change (change.id)}
        <li class="item">
          <div class="what">
            <span class="label">{change.label}</span>
            <span class="when">{relativeTime(change.at)}</span>
            {#if change.effect}<span class="effect">{change.effect}</span>{/if}
          </div>
        </li>
      {/each}
    </ul>
  </section>
{/if}

{#if enabled || items.length > 0}
  <section class="auto" aria-label="Changes made automatically">
    <h3 class="title">Made automatically</h3>
    {#if items.length === 0}
      <p class="note">
        Automatic apply is on. Nothing has been changed yet: a proposal has to stand for two hours
        first, and a pool or host is changed at most once a day.
      </p>
    {:else}
      <p class="note">
        {pluralise(items.length, 'change')} in the last 30 days. An undone change is not made again.
      </p>
      <ul class="items">
        {#each items as change (change.id)}
          <li class="item">
            <div class="what">
              <span class="label">{change.label}</span>
              <span class="when">{relativeTime(change.at)}</span>
              {#if change.effect}<span class="effect">{change.effect}</span>{/if}
            </div>
            {#if change.undone}
              <span class="state">Undone</span>
            {:else if change.undoable && session.can('operator')}
              <Button
                size="sm"
                variant="ghost"
                icon={Undo2}
                loading={busy === change.id}
                onclick={() => undo(change)}
              >
                Undo
              </Button>
            {:else}
              <span class="state">Edited since</span>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>
{/if}

<style>
  .auto {
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .title {
    margin: 0;
    padding: var(--z-space-2) var(--z-space-4);
    background: var(--z-surface-sunken);
    border-bottom: var(--z-border-width) solid var(--z-border);
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text-muted);
  }
  .note {
    margin: 0;
    padding: var(--z-space-3) var(--z-space-4) 0;
    max-width: 62ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }
  .items {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .item {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  .what {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .label {
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .when,
  .effect,
  .state {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
</style>

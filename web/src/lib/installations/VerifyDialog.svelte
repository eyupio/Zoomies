<!--
  What the credentials can actually do.

  A verify is only useful if it says which permission is missing, so this
  renders the probe's own lists by name: the permissions GitHub reports for the
  installation, the events it will send, and the ones Zoomies needs and did not
  get. Missing entries come first, because they are the reason somebody opened
  this.
-->
<script lang="ts">
  import { Check, X } from '@lucide/svelte';
  import type { Installation, InstallationHealth } from '$lib/api/types';
  import { formatNumber, joinWords } from '$lib/format';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  interface Props {
    open?: boolean;
    installation: Installation | null;
    health: InstallationHealth | null;
    loading?: boolean;
    /** Set when the probe itself could not be made. */
    error?: string;
    onclose?: () => void;
  }

  let {
    open = $bindable(false),
    installation,
    health,
    loading = false,
    error = '',
    onclose,
  }: Props = $props();

  const permissions = $derived(Object.entries(health?.permissions ?? {}));
  const missingPermissions = $derived(health?.missing_permissions ?? []);
  const missingEvents = $derived(health?.missing_events ?? []);
  const events = $derived(health?.events ?? []);
  const ok = $derived(health?.ok === true);
</script>

<Dialog
  bind:open
  title="Verify {installation?.target || 'installation'}"
  description="Zoomies asked GitHub what these credentials can do."
  size="md"
  {onclose}
>
  {#if loading}
    <div class="stack" aria-busy="true">
      <Skeleton width="60%" height="1.25rem" />
      <Skeleton lines={4} />
    </div>
  {:else if error}
    <p class="verdict bad">{error}</p>
  {:else if health}
    <div class="stack">
      <p class="verdict" class:bad={!ok} class:good={ok}>
        {#if ok}
          <Check size={15} aria-hidden="true" />
        {:else}
          <X size={15} aria-hidden="true" />
        {/if}
        <span>
          {health.message ||
            (ok
              ? 'The credentials work and every permission Zoomies needs is granted.'
              : 'The credentials did not work.')}
        </span>
      </p>

      {#if health.app_name || health.app_slug}
        <p class="app">
          App: <span class="mono">{health.app_slug || health.app_name}</span>
          {#if health.rate_limit_remaining !== undefined}
            <span class="muted"
              >· {formatNumber(health.rate_limit_remaining)} API requests left in this window</span
            >
          {/if}
        </p>
      {/if}

      {#if missingPermissions.length > 0}
        <section aria-labelledby="missing-permissions">
          <h3 id="missing-permissions" class="bad-heading">Permissions Zoomies still needs</h3>
          <ul class="chips missing">
            {#each missingPermissions as name (name)}
              <li class="mono">{name}</li>
            {/each}
          </ul>
          <p class="fix">
            Open the App's settings on GitHub, grant {joinWords([...missingPermissions])}, then
            accept the updated permissions on the installation.
          </p>
        </section>
      {/if}

      {#if missingEvents.length > 0}
        <section aria-labelledby="missing-events">
          <h3 id="missing-events" class="bad-heading">Events Zoomies is not subscribed to</h3>
          <ul class="chips missing">
            {#each missingEvents as name (name)}
              <li class="mono">{name}</li>
            {/each}
          </ul>
          <p class="fix">
            Without {joinWords([...missingEvents])} the controller only learns about jobs when it polls,
            which is slower and uses more of the API quota.
          </p>
        </section>
      {/if}

      {#if permissions.length > 0}
        <section aria-labelledby="granted-permissions">
          <h3 id="granted-permissions">Permissions granted</h3>
          <ul class="chips">
            {#each permissions as [name, level] (name)}
              <li class="mono">{name}: {level}</li>
            {/each}
          </ul>
        </section>
      {/if}

      {#if events.length > 0}
        <section aria-labelledby="subscribed-events">
          <h3 id="subscribed-events">Events subscribed</h3>
          <ul class="chips">
            {#each events as name (name)}
              <li class="mono">{name}</li>
            {/each}
          </ul>
        </section>
      {/if}
    </div>
  {/if}

  {#snippet footer()}
    <Button
      variant="primary"
      onclick={() => {
        open = false;
        onclose?.();
      }}
    >
      Close
    </Button>
  {/snippet}
</Dialog>

<style>
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-bottom: var(--z-space-2);
  }
  .verdict {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: 0;
    padding: var(--z-space-3);
    border-radius: var(--z-radius-sm);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
  }
  .verdict.good {
    border: 1px solid var(--z-idle-border);
    background: var(--z-idle-subtle);
    color: var(--z-text);
  }
  .verdict.bad {
    border: 1px solid var(--z-danger-border);
    background: var(--z-danger-subtle);
    color: var(--z-text);
  }
  .app {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  h3 {
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  h3.bad-heading {
    color: var(--z-danger);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .chips li {
    padding: 0 var(--z-space-2);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-sm);
  }
  .chips.missing li {
    border-color: var(--z-danger-border);
    background: var(--z-danger-subtle);
    color: var(--z-text);
  }
  .fix {
    margin: var(--z-space-2) 0 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .muted {
    color: var(--z-text-subtle);
  }
</style>

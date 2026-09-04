<!--
  One host.

  A card rather than a table row: a host carries a set of facts that do not fit
  a grid -- capacity, health, backend capabilities and labels -- and there are
  usually few enough hosts that density is not the constraint. Everything an
  operator would act on is here, and the state is carried by a shape and a word
  as well as by a colour.
-->
<script lang="ts">
  import { Pencil, ServerCog, Trash2 } from '@lucide/svelte';
  import type { Host } from '$lib/api/types';
  import { formatNumber, pluralise } from '$lib/format';
  import { hostStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import DropdownMenu from '$lib/components/DropdownMenu.svelte';
  import type { MenuItem } from '$lib/components/DropdownMenu.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import UtilisationBar from '$lib/components/UtilisationBar.svelte';
  import BackendList from './BackendList.svelte';

  interface Props {
    host: Host;
    canOperate?: boolean;
    canAdmin?: boolean;
    oncordon: (host: Host, cordoned: boolean) => void;
    onedit: (host: Host) => void;
    ondelete: (host: Host) => void;
    class?: string;
  }

  let {
    host,
    canOperate = false,
    canAdmin = false,
    oncordon,
    onedit,
    ondelete,
    class: className = '',
  }: Props = $props();

  const status = $derived(hostStatus({ healthy: host.healthy, cordoned: host.cordoned }));
  // The host's own count, not one derived from the cached runner list: the cache
  // holds a page of runners, so counting it would undercount a busy host.
  const active = $derived(host.active_runners ?? 0);
  const capacity = $derived(host.capacity ?? 0);
  const free = $derived(host.free ?? Math.max(0, capacity - active));
  const labels = $derived(Object.entries(host.labels ?? {}));
  const platform = $derived([host.os, host.arch].filter(Boolean).join('/'));

  const actions = $derived<MenuItem[]>([
    {
      id: 'cordon',
      label: host.cordoned ? 'Uncordon this host' : 'Cordon this host',
      icon: ServerCog,
      disabled: !canOperate,
      onSelect: () => oncordon(host, !host.cordoned),
    },
    {
      id: 'edit',
      label: 'Edit capacity and labels',
      icon: Pencil,
      disabled: !canOperate,
      onSelect: () => onedit(host),
    },
    {
      id: 'delete',
      label: 'Remove this host',
      icon: Trash2,
      danger: true,
      separated: true,
      disabled: !canAdmin || host.embedded === true,
      onSelect: () => ondelete(host),
    },
  ]);
</script>

<article class="card {className}" aria-labelledby="host-{host.id}-name">
  <header>
    <div class="identity">
      <h3 id="host-{host.id}-name">{host.name || host.id}</h3>
      <div class="badges">
        <Badge {status} size="sm" title={status.hint} />
        {#if host.embedded}
          <Badge
            tone="accent"
            label="Embedded"
            size="sm"
            dot={false}
            title="This agent runs inside the controller process, so it cannot be removed."
          />
        {/if}
      </div>
    </div>
    <DropdownMenu items={actions} label="Actions for {host.name || host.id}" size="sm" />
  </header>

  <p class="meta">
    {#if platform}<span class="mono">{platform}</span>{/if}
    {#if host.version}<span>agent {host.version}</span>{/if}
    {#if host.address}<span class="mono">{host.address}</span>{/if}
  </p>

  <p class="health" class:bad={host.healthy === false}>
    {#if host.healthy === false}
      No heartbeat since <RelativeTime value={host.last_heartbeat} plain />. The agent is not
      reporting, so no runner will be placed here until it does.
    {:else}
      Healthy · last heartbeat <RelativeTime value={host.last_heartbeat} plain />
    {/if}
  </p>

  {#if host.cordoned}
    <p class="cordoned">
      Cordoned. Its {pluralise(active, 'runner')} keep going and finish their jobs; no new runner is placed
      here until it is uncordoned.
    </p>
  {/if}

  <div class="capacity">
    <UtilisationBar
      busy={active}
      live={capacity}
      label="Runner slots in use on {host.name || host.id}"
      showText={false}
    />
    <p class="capacity-text tabular">
      <strong>{formatNumber(active)}</strong> of {formatNumber(capacity)} slots in use
      <span class="muted">· {formatNumber(free)} free</span>
    </p>
  </div>

  <section class="block" aria-label="Backends on {host.name || host.id}">
    <h4>Backends</h4>
    <BackendList backends={host.backend_info} kinds={host.backends} />
  </section>

  <section class="block" aria-label="Labels on {host.name || host.id}">
    <h4>Labels</h4>
    {#if labels.length === 0}
      <p class="none">None. Pools that select hosts by label will not choose this one.</p>
    {:else}
      <ul class="labels">
        {#each labels as [key, value] (key)}
          <li class="mono">{key}={value}</li>
        {/each}
      </ul>
    {/if}
  </section>
</article>

<style>
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: var(--z-space-5);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    min-width: 0;
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-3);
  }
  .identity {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    min-width: 0;
  }
  h3 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .badges {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-3);
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .health {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .health.bad {
    color: var(--z-danger);
  }
  .cordoned {
    margin: 0;
    padding: var(--z-space-2) var(--z-space-3);
    border: 1px solid var(--z-draining-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-draining-subtle);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .capacity {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
  }
  .capacity-text {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .capacity-text strong {
    color: var(--z-text);
    font-weight: var(--z-weight-semibold);
  }
  .muted {
    color: var(--z-text-subtle);
  }
  .block {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding-top: var(--z-space-3);
    border-top: 1px solid var(--z-border);
  }
  h4 {
    margin: 0;
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-text-muted);
    font-weight: var(--z-weight-medium);
  }
  .labels {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .labels li {
    padding: 0 var(--z-space-1);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
  }
  .none {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
</style>

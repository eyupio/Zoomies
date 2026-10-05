<!--
  Which machines these runners may land on.

  Placement is its own section, ahead of the runner's: an operator picks the
  machines and then decides how a runner is run on them, and the runner's
  "offered by N hosts" counts only mean something once it is known which hosts
  are in play. Choosing a backend first and finding out afterwards that no arm64
  box offers it is the wrong order to learn it in, and on one page the order is
  the order the sections are read in.
-->
<script lang="ts">
  import { ServerCog } from '@lucide/svelte';
  import type { Host, Result } from '$lib/api/types';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import HostSelectorEditor from './HostSelectorEditor.svelte';
  import PoolFit from './PoolFit.svelte';
  import PoolRoom from './PoolRoom.svelte';
  import type { PoolDraft } from './draft';

  interface Props {
    draft: PoolDraft;
    touch: (field: string) => void;
    hosts: readonly Host[];
    /** False until the fleet cache has landed, so we do not cry wolf. */
    hostsKnown: boolean;
    /** The controller's own count, which knows the rules this section does not. */
    verdict: Result<'validatePool'> | null;
    validating: boolean;
  }

  let { draft, touch, hosts, hostsKnown, verdict, validating }: Props = $props();

  // Only when it has something the selector's own count does not. The two agree
  // on a fleet where every matching host can take the work, and a second box
  // saying so in different words is noise in the section where the selector is
  // being typed.
  const shortfall = $derived((verdict?.excluded_hosts ?? []).length > 0);

  /*
    What the machines this pool now reaches can hold, at the size it is asking
    for. Choosing hosts and choosing how big a runner is are one decision taken
    in two places, so the count is here as well as in the Size section: narrowing
    a selector to the two small boxes is the moment to find out that it leaves
    room for three runners, not after the pool exists.
  */
  const room = $derived(verdict?.room ?? null);
  // A pool that takes its size from each host has none of its own to count
  // runners of: the sliders are holding a position nobody chose, and each row
  // of the room says what a runner is on that host instead.
  const profile = $derived(draft.sizing === 'profile');
  const cpus = $derived(profile ? 0 : Number(draft.cpus) || 0);
  const memoryMb = $derived(profile ? 0 : Number(draft.memory_mb) || 0);

  function change(next: Record<string, string>): void {
    draft.host_selector = next;
    touch('host_selector');
  }

  function restrict(next: boolean): void {
    draft.restrict_hosts = next;
  }
</script>

{#if !hostsKnown}
  <div aria-busy="true">
    <Skeleton width="40%" height="0.9rem" />
    <Skeleton lines={3} />
  </div>
{:else if hosts.length === 0}
  <!--
    Nothing to choose between yet. Saying so beats an empty dropdown labelled
    "Any operating system", which reads like a fleet that has no Linux in it.
  -->
  <div class="empty">
    <p class="empty-title">
      <ServerCog size={16} aria-hidden="true" />
      No host has joined yet
    </p>
    <p class="empty-body">
      This pool will reach every host that joins. Once machines are connected you can come back and
      keep it to some of them — an architecture, an operating system, or your own labels.
    </p>
  </div>
{:else}
  <HostSelectorEditor
    selector={draft.host_selector}
    {hosts}
    {hostsKnown}
    restricting={draft.restrict_hosts}
    onchange={change}
    onrestrict={restrict}
  />
  {#if shortfall}
    <PoolFit {verdict} {validating} />
  {/if}
  {#if profile || (cpus > 0 && memoryMb > 0)}
    <PoolRoom {room} {cpus} {memoryMb} {profile} {validating} />
  {/if}
{/if}

<style>
  .empty {
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .empty-title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .empty-body {
    margin: var(--z-space-2) 0 0;
    max-width: 66ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
</style>

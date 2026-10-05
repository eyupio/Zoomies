<!--
  Step six: how many runners, and for how long.

  It comes after the size on purpose. A maximum is a number about machines --
  it means something only once it is known what one runner costs and what the
  hosts can hold -- and asked first it was a number typed into an empty box
  with nothing on screen able to say what it would buy. By the time this step
  is reached the fleet has an answer, so the maximum starts at what the hosts
  can actually place and says where the figure came from.

  It stays a figure rather than becoming "as many as fit". The maximum is a
  backstop: it is what stops one misconfigured workflow, or one repository
  under a fork-pull-request storm, filling every machine and every rented one
  behind it. A cap that silently grew with the fleet would be a cap nobody
  chose, so the fleet's room is offered and an operator accepts it.
-->
<script lang="ts">
  import { Sparkles } from '@lucide/svelte';
  import { formatGoDuration, parseGoDuration, pluralise } from '$lib/format';
  import type { PoolRoom as PoolRoomShape, Result } from '$lib/api/types';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import PoolRoom from './PoolRoom.svelte';
  import { sizeLabel } from './sizing';
  import type { PoolDraft } from './draft';

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
    /** What the fleet makes of these limits, asked as they are typed. */
    verdict: Result<'validatePool'> | null;
    validating: boolean;
    /**
     * Whether the maximum is still the one the wizard worked out from the
     * fleet. It stops following the moment an operator types their own.
     */
    following?: boolean;
  }

  let { draft, errors, touch, verdict, validating, following = false }: Props = $props();

  const timeout = $derived(parseGoDuration(draft.idle_timeout));
  const timeoutText = $derived(timeout === null ? '' : formatGoDuration(draft.idle_timeout));

  // As on the hosts step: a pool that takes its size from each host has none
  // of its own to count runners of.
  const profile = $derived(draft.sizing === 'profile');
  const cpus = $derived(profile ? 0 : Number(draft.cpus) || 0);
  const memoryMb = $derived(profile ? 0 : Number(draft.memory_mb) || 0);
  const room = $derived<PoolRoomShape | null>(verdict?.room ?? null);
  const roomTotal = $derived(room?.runners ?? 0);
  const maximum = $derived(Number(draft.max_runners) || 0);
  const minimum = $derived(Number(draft.min_runners) || 0);

  /* The fleet grew, or the runners got smaller, and this pool is not allowed
     to use it. Nothing else says so: the pool is enabled, matches its hosts,
     and simply stops at a number chosen when the fleet was smaller. */
  const roomToSpare = $derived(roomTotal > 0 && maximum < roomTotal);
  /* A minimum above what the fleet can hold is warm runners that never appear,
     which reads on every page as a pool that is permanently short. */
  const minAboveRoom = $derived(roomTotal > 0 && minimum > roomTotal);

  function setMax(value: number): void {
    draft.max_runners = String(value);
    touch('max_runners');
  }
</script>

<div class="pair">
  <Field
    label="Minimum runners"
    error={errors['min_runners']}
    hint="Kept warm even when nothing is queued. Zero means the pool costs nothing while it is idle, at the price of a cold start on the first job."
  >
    {#snippet children({ id, describedBy, invalid })}
      <Input
        bind:value={draft.min_runners}
        {id}
        {describedBy}
        {invalid}
        type="number"
        min={0}
        step={1}
        onblur={() => touch('min_runners')}
      />
    {/snippet}
  </Field>

  <Field
    label="Maximum runners"
    required
    error={errors['max_runners']}
    hint="The backstop. However many jobs GitHub queues, this pool will never create more runners than this — which is what stops one misconfigured workflow filling every host you have."
    notice={following && roomTotal > 0
      ? `Following the fleet: ${pluralise(roomTotal, 'runner')}${profile ? '' : ` of ${sizeLabel(cpus, memoryMb)}`} fit on the hosts this pool reaches. Type your own and it stops following.`
      : undefined}
  >
    {#snippet children({ id, describedBy, invalid })}
      <Input
        bind:value={draft.max_runners}
        {id}
        {describedBy}
        {invalid}
        type="number"
        min={1}
        step={1}
        onblur={() => touch('max_runners')}
      />
    {/snippet}
  </Field>
</div>

<!--
  The same count the size step showed, read the other way round: there it says
  what a size costs, here it says what a cap leaves on the table. Both offer
  the change that resolves it.
-->
<PoolRoom
  {room}
  {cpus}
  {memoryMb}
  {profile}
  {validating}
  maxRunners={maximum}
  onusemax={(value) => setMax(value)}
/>

{#if roomToSpare && !following}
  <div class="spare">
    <p>
      The hosts this pool reaches have room for {pluralise(roomTotal, 'runner')} of this size, and its
      maximum is {maximum}. A burst of jobs will queue behind that cap on machines that are standing
      by.
    </p>
    <Button variant="secondary" size="sm" icon={Sparkles} onclick={() => setMax(roomTotal)}>
      Raise the maximum to {roomTotal}
    </Button>
  </div>
{/if}

{#if minAboveRoom}
  <p class="echo">
    The minimum is above what the fleet can hold, so {pluralise(minimum - roomTotal, 'warm runner')}
    would never appear and this pool would read as permanently short.
  </p>
{/if}

<Field
  label="Priority"
  error={errors['priority']}
  hint="Higher-priority pools receive the global creation budget first. Pools at the same priority share it round-robin."
>
  {#snippet children({ id, describedBy, invalid })}
    <Input
      bind:value={draft.priority}
      {id}
      {describedBy}
      {invalid}
      type="number"
      step={1}
      onblur={() => touch('priority')}
    />
  {/snippet}
</Field>

<Field
  label="Idle timeout"
  error={errors['idle_timeout']}
  hint="How long a runner waits for work before it is destroyed. A Go duration: 5m, 90s, 1h30m."
>
  {#snippet children({ id, describedBy, invalid })}
    <Input
      bind:value={draft.idle_timeout}
      {id}
      {describedBy}
      {invalid}
      mono
      placeholder="5m"
      autocomplete="off"
      onblur={() => touch('idle_timeout')}
    />
  {/snippet}
</Field>

{#if timeoutText}
  <p class="echo">Runners above the minimum are destroyed after {timeoutText} with no work.</p>
{/if}

<Checkbox
  bind:checked={draft.ephemeral}
  label="Destroy each runner after one job"
  description="The safe default. Turning it off reuses a runner between jobs, which is faster and means one job can leave files, credentials or processes behind for the next."
  onchange={() => touch('ephemeral')}
/>

{#if !draft.ephemeral}
  <Checkbox
    bind:checked={draft.no_default_labels}
    label="Leave out the default labels"
    description="Register runners with only this pool’s labels, without self-hosted, the operating system and the architecture. A job whose runs-on asks for one of those no longer lands here unless the pool lists it. GitHub adds them to every ephemeral runner, so this is only offered for a pool that reuses its runners."
    onchange={() => touch('no_default_labels')}
  />
{/if}

<style>
  .pair {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--z-space-5);
    align-items: start;
  }
  .echo {
    margin: 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }
  .spare {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .spare p {
    margin: 0;
    max-width: 66ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  @media (max-width: 768px) {
    .pair {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>

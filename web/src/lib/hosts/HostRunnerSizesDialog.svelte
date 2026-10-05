<!--
  How big a runner is on one host.

  Three figures, each optional: the standard size one runner is given here, for
  a pool that takes its size from the host; the minimum below which no runner is
  placed here; and the most CPU a runner here may be lent. A field left empty
  follows the fleet's runners.* setting, and a host with nothing set behaves
  exactly as it did before there was a profile -- which is how every host starts.

  The count under the fields is what the dialog is for. A standard size is also
  what decides how many runners the host takes -- as many as its machine holds,
  up to its capacity -- so the operator sees what the figure they have just
  chosen does to this host, in this host's own numbers, before they save it. The
  count is worked out here from the machine less its reserve as the controller
  last reported it; the card shows the controller's own once it is saved.

  A profile that would leave a pool with nowhere to run is the one refusal the
  controller makes, and it is the same question the capacity dialog asks: it is
  shown here rather than thrown away as a toast, and saving it anyway is right
  when the pool is on its way out.
-->
<script lang="ts">
  // The sizes a folder is asked for on this host, one row each. `key` is typed to
  // the figures that are numbers, so the form can read and write them by name.
  const FOLDER_SIZES: readonly {
    key: 'tmpfsWorkMb' | 'tmpfsTmpMb' | 'tmpfsDaemonMb';
    name: string;
    label: string;
    placeholder: string;
  }[] = [
    {
      key: 'tmpfsWorkMb',
      name: 'tmpfs.work_mb',
      label: 'Work folder size (MB)',
      placeholder: 'default 4096',
    },
    {
      key: 'tmpfsTmpMb',
      name: 'tmpfs.tmp_mb',
      label: '/tmp size (MB)',
      placeholder: 'default 1024',
    },
    {
      key: 'tmpfsDaemonMb',
      name: 'tmpfs.daemon_mb',
      label: 'Docker image store size (MB)',
      placeholder: 'default 8192',
    },
  ];

  import { untrack } from 'svelte';
  import { ApiError, updateHost } from '$lib/api/client';
  import type { Host } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import QuantityField from '$lib/components/QuantityField.svelte';
  import { CPU_NOTCHES, MEMORY_NOTCHES, cpuLabel, memoryLabel, withValue } from '$lib/pools/sizing';
  import { CircleCheck, Info, TriangleAlert } from '@lucide/svelte';
  import {
    NO_FIGURES,
    figuresOf,
    isUnset,
    machineOf,
    profileBody,
    profileErrors,
    sameFigures,
    slotsFor,
    type ProfileFigures,
  } from './profile';

  interface Props {
    open?: boolean;
    host: Host | null;
    onclose?: () => void;
  }

  let { open = $bindable(false), host, onclose }: Props = $props();

  let figures = $state<ProfileFigures>({ ...NO_FIGURES });
  let saving = $state(false);
  /** The server's field refusals; the ones found while typing come from `profileErrors`. */
  let serverErrors = $state<Record<string, string>>({});
  let loadedFor = $state<string | null>(null);
  /** The controller's refusal, when these sizes would strand a pool. */
  let stranding = $state('');

  // Reload the form when a different host is opened, and only then: an SSE
  // update to the host must not overwrite what is being moved.
  $effect(() => {
    if (!open || !host) {
      loadedFor = null;
      return;
    }
    if (loadedFor === host.id) return;
    loadedFor = host.id ?? null;
    figures = figuresOf(host.runner_profile);
    serverErrors = {};
    stranding = '';
  });

  // A refusal is about the figures that were sent, so moving any of them
  // withdraws it rather than leaving an answer to a stale question beside a
  // "Save anyway" that would now save something else.
  $effect(() => {
    void [
      figures.minCpus,
      figures.minMemoryMb,
      figures.standardCpus,
      figures.standardMemoryMb,
      figures.burstMaxCpus,
      figures.tmpfsOff,
      figures.tmpfsMaxMb,
      figures.tmpfsWorkMb,
      figures.tmpfsTmpMb,
      figures.tmpfsDaemonMb,
    ];
    untrack(() => {
      stranding = '';
      serverErrors = {};
    });
  });

  /* -- the machine, and what the figures do to it -------------------------- */

  const machine = $derived(machineOf(host));
  const capacity = $derived(host?.capacity ?? 0);
  const active = $derived(host?.active_runners ?? 0);
  const count = $derived(slotsFor(machine, capacity, figures));
  const foundErrors = $derived(profileErrors(figures, machine));
  const errors = $derived({ ...serverErrors, ...foundErrors });
  const invalid = $derived(Object.keys(foundErrors).length > 0);
  const unchanged = $derived(sameFigures(figures, figuresOf(host?.runner_profile)));
  const hasStandard = $derived(figures.standardCpus > 0 || figures.standardMemoryMb > 0);
  const standardText = $derived(
    [
      figures.standardCpus > 0 ? cpuLabel(figures.standardCpus) : '',
      figures.standardMemoryMb > 0 ? memoryLabel(figures.standardMemoryMb) : '',
    ]
      .filter(Boolean)
      .join(' and '),
  );
  const machineText = $derived(
    [
      machine.cpus > 0 ? cpuLabel(machine.allocatableCpus) : '',
      machine.memoryMb > 0 ? memoryLabel(machine.allocatableMemoryMb) : '',
    ]
      .filter(Boolean)
      .join(' and '),
  );

  /**
   * The count, in words. Four answers and each says what to do about it: a host
   * that is paused is not changed by a size; one with no standard keeps its
   * capacity; one that has not measured itself cannot be divided; and one with
   * a figure gets the sum, with whichever limit decided it named.
   */
  const summary = $derived.by((): { tone: 'ok' | 'note' | 'warn'; title: string; body: string } => {
    if (capacity <= 0) {
      return {
        tone: 'warn',
        title: 'This host is paused',
        body: 'Its capacity is 0, which takes no runners whatever sizes are set here. A size never un-pauses a host; raise its capacity from "Adjust capacity and reserve" when it should run again.',
      };
    }
    if (!hasStandard) {
      return {
        tone: 'note',
        title: `${pluralise(capacity, 'runner')} at once, as its capacity says`,
        body: 'No standard size is set, so the capacity decides. A pool that takes its size from this host is given the fleet’s default size here. Set a standard and the host takes as many runners of that size as its machine holds, up to its capacity.',
      };
    }
    if (!count.derived) {
      return {
        tone: 'note',
        title: `${pluralise(capacity, 'runner')} at once, as its capacity says`,
        body: 'This host has not reported what machine it is, so a standard cannot yet decide how many runners it takes. It does size the runners of a pool that takes its size from this host, and starts deciding the count on the heartbeat that reports its cores and memory.',
      };
    }
    if (count.slots === 0) {
      return {
        tone: 'warn',
        title: 'No runner of that size fits',
        body: `At ${standardText} each, the ${machineText} this host has to place runners on does not hold one, so it would take none of the work of a pool that sizes from it. Choose a smaller standard.`,
      };
    }
    const limit =
      count.limitedBy === 'cpu'
        ? ' Its cores run out first.'
        : count.limitedBy === 'memory'
          ? ' Its memory runs out first.'
          : count.limitedBy === 'capacity'
            ? ` Its capacity of ${capacity} is below the ${count.held} the machine holds, so the capacity is the limit; raise it to use the rest.`
            : '';
    const running =
      active > count.slots
        ? ` It is running ${pluralise(active, 'runner')} now. They are not interrupted; no new runner is placed here until there is room.`
        : '';
    return {
      tone: 'ok',
      title: `${pluralise(count.slots, 'runner')} at once`,
      body: `At ${standardText} each, the ${machineText} this host has to place runners on holds ${pluralise(count.held, 'runner')}.${limit}${running}`,
    };
  });

  /* -- notches --------------------------------------------------------------- */

  /* The notches stop at what the machine can place: a figure above it is one
     the server refuses, and a slider that offered it would be offering a
     mistake. A host that has not said what it is gets every notch. */
  const cpuNotches = (current: number) =>
    withValue(
      [0, ...CPU_NOTCHES.filter((n) => machine.cpus <= 0 || n <= machine.allocatableCpus)],
      current,
    );
  const memoryNotches = (current: number) =>
    withValue(
      [
        0,
        ...MEMORY_NOTCHES.filter((n) => machine.memoryMb <= 0 || n <= machine.allocatableMemoryMb),
      ],
      current,
    );

  /* What an empty field follows, as the controller resolved it for this host
     the last time it was saved. It is the fleet's figure only where the host
     had left the field empty, so it is offered as a hint and never as a value:
     a field this host sets has no fleet figure to be read back from here. */
  const effective = $derived(host?.effective_profile);
  const fleetCpus = (size: { cpus?: number; cpus_source?: string } | undefined) =>
    size?.cpus_source === 'global' ? (size.cpus ?? 0) : 0;
  const fleetMemory = (size: { memory_mb?: number; memory_mb_source?: string } | undefined) =>
    size?.memory_mb_source === 'global' ? (size.memory_mb ?? 0) : 0;
  const followsCpus = (size: { cpus?: number; cpus_source?: string } | undefined) => {
    const cpus = fleetCpus(size);
    return cpus > 0 ? `${cpuLabel(cpus)}, the fleet's` : 'the fleet';
  };
  const followsMemory = (size: { memory_mb?: number; memory_mb_source?: string } | undefined) => {
    const memory = fleetMemory(size);
    return memory > 0 ? `${memoryLabel(memory)}, the fleet's` : 'the fleet';
  };

  function clear(): void {
    figures = { ...NO_FIGURES };
  }

  function close(): void {
    open = false;
    onclose?.();
  }

  async function save(confirm = false): Promise<void> {
    if (!host?.id || invalid) return;
    saving = true;
    serverErrors = {};
    try {
      await updateHost(
        host.id,
        { runner_profile: profileBody(figures) },
        confirm ? { confirm: true } : undefined,
      );
      await fleet.reconcile();
      const name = host.name || host.id;
      if (isUnset(figures)) {
        toasts.success(
          `${name} follows the fleet again`,
          'No runner sizes are set on it. The scheduler uses that on its next pass.',
        );
      } else {
        toasts.success(
          `${name} runner sizes saved`,
          'The scheduler uses them on its next pass. Runners already running keep the size they were given.',
        );
      }
      close();
    } catch (cause) {
      // The stranding refusal is a question, not a failure: it is answered in
      // the dialog, so it does not become a toast that closes over the form
      // the answer is in.
      if (cause instanceof ApiError && cause.isConflict) {
        stranding = cause.message;
        return;
      }
      if (cause instanceof ApiError) serverErrors = cause.fieldErrors();
      toasts.fromError(cause, 'That host was not adjusted');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  title="Runner sizes on {host?.name || 'host'}"
  description="How big a runner is here. Anything left empty follows the fleet's setting."
  onclose={close}
>
  <form
    id="host-runner-sizes-form"
    class="form"
    onsubmit={(event) => {
      event.preventDefault();
      void save();
    }}
  >
    <div
      class="summary"
      class:warn={summary.tone === 'warn'}
      class:note={summary.tone === 'note'}
      role="status"
      aria-live="polite"
      data-testid="runner-sizes-summary"
    >
      {#if summary.tone === 'ok'}
        <CircleCheck size={16} aria-hidden="true" />
      {:else if summary.tone === 'warn'}
        <TriangleAlert size={16} aria-hidden="true" />
      {:else}
        <Info size={16} aria-hidden="true" />
      {/if}
      <div>
        <p class="title">{summary.title}</p>
        <p>{summary.body}</p>
      </div>
    </div>

    <fieldset class="group">
      <legend>Standard size</legend>
      <p class="note-text">
        What one runner is given here for a pool that takes its size from the host. It is also what
        sets how many runners the host takes: as many as its machine holds, up to its capacity. A
        pool that states a size of its own is not changed by it.
      </p>
      <div class="pair">
        <Field
          label="Standard CPU"
          error={errors['runner_profile.standard.cpus'] ?? ''}
          hint="Empty follows {followsCpus(effective?.standard)}."
        >
          {#snippet children({ id, describedBy, invalid: bad })}
            <QuantityField
              {id}
              quantity="cpus"
              values={cpuNotches(figures.standardCpus)}
              value={figures.standardCpus}
              label="Standard CPU per runner"
              valuetext={(v) => (v === 0 ? 'the fleet default' : cpuLabel(v))}
              marks={[{ value: 0, label: 'fleet' }]}
              empty={{ value: 0, placeholder: 'fleet default' }}
              {describedBy}
              invalid={bad}
              onchange={(v) => (figures.standardCpus = v ?? 0)}
            />
          {/snippet}
        </Field>
        <Field
          label="Standard memory"
          error={errors['runner_profile.standard.memory_mb'] ?? ''}
          hint="Empty follows {followsMemory(effective?.standard)}."
        >
          {#snippet children({ id, describedBy, invalid: bad })}
            <QuantityField
              {id}
              quantity="mb"
              values={memoryNotches(figures.standardMemoryMb)}
              value={figures.standardMemoryMb}
              label="Standard memory per runner"
              valuetext={(v) => (v === 0 ? 'the fleet default' : memoryLabel(v))}
              marks={[{ value: 0, label: 'fleet' }]}
              empty={{ value: 0, placeholder: 'fleet default' }}
              {describedBy}
              invalid={bad}
              onchange={(v) => (figures.standardMemoryMb = v ?? 0)}
            />
          {/snippet}
        </Field>
      </div>
      <Field
        label="Boost ceiling"
        error={errors['runner_profile.standard.burst_max_cpus'] ?? ''}
        hint="The most CPU one runner here may use, its own share and any CPU lent to it together. A pool's own ceiling can lower it and never raise it. Empty sets no ceiling of this host's."
      >
        {#snippet children({ id, describedBy, invalid: bad })}
          <QuantityField
            {id}
            quantity="cpus"
            values={cpuNotches(figures.burstMaxCpus)}
            value={figures.burstMaxCpus}
            label="Boost ceiling per runner"
            valuetext={(v) => (v === 0 ? 'no ceiling of this host’s' : cpuLabel(v))}
            marks={[{ value: 0, label: 'none' }]}
            empty={{ value: 0, placeholder: 'No ceiling' }}
            {describedBy}
            invalid={bad}
            onchange={(v) => (figures.burstMaxCpus = v ?? 0)}
          />
        {/snippet}
      </Field>
      <Field
        label="Memory ceiling"
        error={errors['runner_profile.standard.burst_max_memory_mb'] ?? ''}
        hint="The most memory one runner here may hold, its own share and any memory lent to it together. A pool's own ceiling can lower it and never raise it. Empty sets no ceiling of this host's."
      >
        {#snippet children({ id, describedBy, invalid: bad })}
          <QuantityField
            {id}
            quantity="mb"
            values={memoryNotches(figures.burstMaxMemoryMb)}
            value={figures.burstMaxMemoryMb}
            label="Memory ceiling per runner"
            valuetext={(v) => (v === 0 ? 'no ceiling of this host’s' : memoryLabel(v))}
            marks={[{ value: 0, label: 'none' }]}
            empty={{ value: 0, placeholder: 'No ceiling' }}
            {describedBy}
            invalid={bad}
            onchange={(v) => (figures.burstMaxMemoryMb = v ?? 0)}
          />
        {/snippet}
      </Field>
    </fieldset>

    <fieldset class="group">
      <legend>Minimum</legend>
      <p class="note-text">
        The least a runner is given here. It is a floor under a pool's own minimum, never a ceiling
        over it, and a pool that states a size below it is not placed on this host at all: a pool
        that states its size is never given more than it states.
      </p>
      <div class="pair">
        <Field
          label="Minimum CPU"
          error={errors['runner_profile.minimum.cpus'] ?? ''}
          hint="Empty follows {followsCpus(effective?.minimum)}."
        >
          {#snippet children({ id, describedBy, invalid: bad })}
            <QuantityField
              {id}
              quantity="cpus"
              values={cpuNotches(figures.minCpus)}
              value={figures.minCpus}
              label="Minimum CPU per runner"
              valuetext={(v) => (v === 0 ? 'the fleet default' : cpuLabel(v))}
              marks={[{ value: 0, label: 'fleet' }]}
              empty={{ value: 0, placeholder: 'fleet default' }}
              {describedBy}
              invalid={bad}
              onchange={(v) => (figures.minCpus = v ?? 0)}
            />
          {/snippet}
        </Field>
        <Field
          label="Minimum memory"
          error={errors['runner_profile.minimum.memory_mb'] ?? ''}
          hint="Empty follows {followsMemory(effective?.minimum)}."
        >
          {#snippet children({ id, describedBy, invalid: bad })}
            <QuantityField
              {id}
              quantity="mb"
              values={memoryNotches(figures.minMemoryMb)}
              value={figures.minMemoryMb}
              label="Minimum memory per runner"
              valuetext={(v) => (v === 0 ? 'the fleet default' : memoryLabel(v))}
              marks={[{ value: 0, label: 'fleet' }]}
              empty={{ value: 0, placeholder: 'fleet default' }}
              {describedBy}
              invalid={bad}
              onchange={(v) => (figures.minMemoryMb = v ?? 0)}
            />
          {/snippet}
        </Field>
      </div>
    </fieldset>

    <fieldset class="group">
      <legend>In-memory folders</legend>
      <p class="note-text">
        A pool can ask to keep a runner's work folder, <code>/tmp</code> or Docker image store in memory
        instead of on disk. A pool is one setting for every host it lands on, and this host's memory is
        its owner's to judge, so here you have the last word. A runner already running is not changed.
      </p>
      <Checkbox
        bind:checked={figures.tmpfsOff}
        label="Fall back to disk on this machine (temporary)"
        description="A tactical fix, for a machine that cannot spare the memory today: whatever a pool asks for, its runners here keep their folders on disk, as they did before the setting existed. The pool is told for as long as this is on, so it is not forgotten. The lasting answers are the sizes below and a pool's Auto placement."
      />
      {#if !figures.tmpfsOff}
        <p class="note-text">
          Sizes a folder is asked for on this machine when a pool leaves it to size itself — the
          counterpart of a standard runner size. A machine with a great deal of memory can offer far
          more than the defaults, and one with little, less. Each is still fitted to the runner's
          limit and lowered to the ceiling below; leave one empty to use the default.
        </p>
        {#each FOLDER_SIZES as f (f.key)}
          <Field label={f.label} error={errors[`runner_profile.${f.name}`] ?? ''}>
            {#snippet children({ id, describedBy, invalid: bad })}
              <Input
                value={figures[f.key] > 0 ? String(figures[f.key]) : ''}
                {id}
                {describedBy}
                invalid={bad}
                inputmode="numeric"
                placeholder={f.placeholder}
                autocomplete="off"
                oninput={(event) => {
                  const n = Number((event.currentTarget as HTMLInputElement).value.trim());
                  figures[f.key] = Number.isInteger(n) && n > 0 ? n : 0;
                }}
              />
            {/snippet}
          </Field>
        {/each}
        <Field
          label="Largest folder (MB)"
          error={errors['runner_profile.tmpfs.max_mb'] ?? ''}
          hint="The most any one in-memory folder may be on this machine, whatever its pool asks for. A pool's size can lower it and never raise it. Empty sets no ceiling of this host's."
        >
          {#snippet children({ id, describedBy, invalid: bad })}
            <Input
              value={figures.tmpfsMaxMb > 0 ? String(figures.tmpfsMaxMb) : ''}
              {id}
              {describedBy}
              invalid={bad}
              inputmode="numeric"
              placeholder="no ceiling"
              autocomplete="off"
              oninput={(event) => {
                const n = Number((event.currentTarget as HTMLInputElement).value.trim());
                figures.tmpfsMaxMb = Number.isInteger(n) && n > 0 ? n : 0;
              }}
            />
          {/snippet}
        </Field>
      {/if}
    </fieldset>

    {#if !isUnset(figures)}
      <div class="clear">
        <Button variant="ghost" size="sm" onclick={clear}>Follow the fleet in everything</Button>
      </div>
    {/if}

    {#if stranding}
      <div class="refusal" role="alert">
        <TriangleAlert size={16} aria-hidden="true" />
        <div>
          <p class="title">Not saved: a pool would have nowhere to run</p>
          <p>{stranding}</p>
        </div>
      </div>
    {/if}
  </form>

  {#snippet footer()}
    <Button variant="ghost" onclick={close}>Cancel</Button>
    {#if stranding}
      <Button variant="danger" loading={saving} onclick={() => void save(true)}>Save anyway</Button>
    {:else}
      <Button
        variant="primary"
        type="submit"
        form="host-runner-sizes-form"
        loading={saving}
        disabled={invalid || unchanged}
      >
        Save sizes
      </Button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-bottom: var(--z-space-2);
  }
  /* The count: what the figures below do to this host. It takes the idle
     colours when a runner of that size fits, the pending ones when something
     needs attending to, and the neutral surface when it is only saying that
     nothing has changed -- the status colours are a fixed mapping, and a note
     is not a state. */
  .summary {
    display: flex;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-idle-border);
    border-radius: var(--z-radius-md);
    background: var(--z-idle-subtle);
    color: var(--z-idle);
  }
  .summary.note {
    border-color: var(--z-border);
    background: var(--z-surface-sunken);
    color: var(--z-text-muted);
  }
  .summary.warn {
    border-color: var(--z-pending-border);
    background: var(--z-pending-subtle);
    color: var(--z-pending);
  }
  .summary p {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .summary .title {
    color: inherit;
    font-weight: var(--z-weight-semibold);
    margin-bottom: var(--z-nudge-2);
  }
  .summary :global(svg) {
    flex: none;
    margin-top: var(--z-nudge-2);
  }
  .group {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    margin: 0;
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
  }
  legend {
    padding: 0 var(--z-space-1);
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .note-text {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }
  .pair {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--z-space-4);
    align-items: start;
  }
  .clear {
    display: flex;
    justify-content: flex-start;
  }
  /* The controller's refusal, which is not the same thing as a figure that is
     out of range: nothing was saved, so it is coloured danger. */
  .refusal {
    display: flex;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-danger-border);
    border-radius: var(--z-radius-md);
    background: var(--z-danger-subtle);
  }
  .refusal p {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .refusal .title {
    color: var(--z-danger);
    font-weight: var(--z-weight-semibold);
    margin-bottom: var(--z-nudge-2);
  }
  .refusal :global(svg) {
    flex: none;
    margin-top: var(--z-nudge-2);
    color: var(--z-danger);
  }
  @media (max-width: 768px) {
    .pair {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>

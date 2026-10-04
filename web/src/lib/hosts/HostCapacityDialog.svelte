<!--
  Adjust a host: how many runners it may hold, and what it keeps for itself.

  Four settings, each on a slider that moves between notches, with the
  recommendation marked on it and a sentence that says where the
  recommendation came from -- the machine's size, less the reserve, divided
  by what a runner in this fleet asks for. A setting past its recommendation
  says so in a callout rather than refusing: an operator who knows the jobs
  are light is right to go past it, and one who does not is told what it
  costs. "Set to recommendations" puts all four back in one press.

  Only the fields the host has reported are offered. A reserve held back from
  a figure nobody has measured is a number the scheduler ignores, and a host
  that has not said how big it is gets no recommendation rather than a made
  up one.

  A reserve that would leave a pool with nowhere to run at all is the one the
  controller refuses, and the refusal is shown here rather than thrown away as
  a toast: it names the pool and the shortfall, and the operator either goes
  back to the slider or says they meant it. Saving it anyway is right when the
  pool is on its way out, so the button is offered rather than the door shut.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { ApiError, updateHost } from '$lib/api/client';
  import type { Host } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import Slider from '$lib/components/Slider.svelte';
  import QuantityField from '$lib/components/QuantityField.svelte';
  import { cpuLabel, memoryLabel as sizeMemoryLabel, withValue } from '$lib/pools/sizing';
  import { Sparkles, TriangleAlert } from '@lucide/svelte';
  import {
    capacityCeiling,
    diskNotches,
    memoryLabel,
    memoryNotches,
    overcommit,
    recommendedCapacity,
    recommendedReserveCores,
    recommendedReserveDiskMb,
    recommendedReserveMemoryMb,
    runnerAsk,
  } from './recommend';

  interface Props {
    open?: boolean;
    host: Host | null;
    onclose?: () => void;
  }

  let { open = $bindable(false), host, onclose }: Props = $props();
  let capacity = $state('');
  let reserveCores = $state(0);
  let reserveMb = $state(0);
  let reserveDiskMb = $state(0);
  let saving = $state(false);
  let errors = $state<Record<string, string>>({});
  let loadedFor = $state<string | null>(null);
  /** The controller's refusal, when this reserve would strand a pool. */
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
    capacity = String(host.capacity ?? 0);
    reserveCores = host.reserve_cpus ?? 0;
    reserveMb = host.reserve_memory_mb ?? 0;
    reserveDiskMb = host.reserve_disk_mb ?? 0;
    errors = {};
    stranding = '';
  });

  // A refusal is about the figures that were sent, so moving any of them
  // withdraws the question rather than leaving an answer to a stale one on
  // screen beside a "Save anyway" that would now save something else.
  $effect(() => {
    void [capacity, reserveCores, reserveMb, reserveDiskMb];
    stranding = '';
  });

  // The capacity refusal also blocks Save, so it goes when the capacity is
  // edited, and only then: moving a reserve must not withdraw an answer about
  // something else. errors is read untracked so a refusal that has just
  // arrived is not cleared by the effect it did not come from.
  $effect(() => {
    void capacity;
    untrack(() => {
      if (errors.capacity) delete errors.capacity;
    });
  });

  /* -- the machine, and what a runner asks of it ----------------------------- */

  const shape = $derived({ cpus: host?.cpus ?? 0, memoryMb: host?.memory_mb ?? 0 });
  /** The work directory's filesystem, which the capacity recommendation does
      not follow -- a pool asks for cores and memory, never for disk -- but
      which runs out first and says nothing when it does. */
  const diskTotalMb = $derived(host?.disk_total_mb ?? 0);
  const sized = $derived(shape.cpus > 0 || shape.memoryMb > 0 || diskTotalMb > 0);
  // Sized against what this fleet's pools actually ask for, and where none
  // has said, against the fleet's own default rather than a figure of this
  // page's own -- so the capacity recommended here and the sliders the pool
  // wizard opens on are describing the same fleet.
  const ask = $derived(runnerAsk(fleet.pools, fleet.runnerDefaults));
  const recCores = $derived(recommendedReserveCores(shape.cpus));
  const recMb = $derived(recommendedReserveMemoryMb(shape.memoryMb));
  const recDiskMb = $derived(recommendedReserveDiskMb(diskTotalMb));
  const recCapacity = $derived(recommendedCapacity(shape, reserveCores, reserveMb, ask));

  /* -- the standard size ----------------------------------------------------------
     A host that has been given a standard runner size takes as many runners of
     it as its machine holds, and the capacity below is only a ceiling on that.
     An operator who moves the slider without knowing it would be editing a
     number that is not the one binding, so the dialog says what is. */
  const standard = $derived(host?.runner_profile?.standard);
  const hasStandard = $derived((standard?.cpus ?? 0) > 0 || (standard?.memory_mb ?? 0) > 0);
  const standardText = $derived(
    [
      (standard?.cpus ?? 0) > 0 ? cpuLabel(standard?.cpus ?? 0) : '',
      (standard?.memory_mb ?? 0) > 0 ? sizeMemoryLabel(standard?.memory_mb ?? 0) : '',
    ]
      .filter(Boolean)
      .join(' and '),
  );

  /* -- capacity ---------------------------------------------------------------- */

  const parsed = $derived(Number(capacity));
  const capacityError = $derived(
    errors.capacity ??
      (capacity.trim() === ''
        ? 'Give a number. Use 0 to pause new runner placement on this host.'
        : !Number.isInteger(parsed) || parsed < 0
          ? 'Runner capacity must be a whole number of zero or more.'
          : ''),
  );
  const ceiling = $derived(
    capacityCeiling(shape, recCapacity, Number.isFinite(parsed) ? parsed : 0),
  );
  const capacityNotches = $derived(Array.from({ length: ceiling + 1 }, (_, i) => i));
  const capacityMarks = $derived.by(() => {
    const marks: { value: number; label: string; recommended?: boolean }[] = [
      { value: 0, label: 'paused' },
    ];
    if (recCapacity > 0)
      marks.push({ value: recCapacity, label: 'recommended', recommended: true });
    if (shape.cpus > 0 && shape.cpus <= ceiling && shape.cpus !== recCapacity)
      marks.push({ value: shape.cpus, label: 'one per core' });
    if (!marks.some((m) => m.value === ceiling))
      marks.push({ value: ceiling, label: String(ceiling) });
    return marks.sort((a, b) => a.value - b.value);
  });
  const over = $derived(
    overcommit(shape, Number.isFinite(parsed) ? parsed : 0, reserveCores, reserveMb, ask),
  );
  const aboveCapacity = $derived(recCapacity > 0 && parsed > recCapacity);

  const gb = (mb: number) =>
    `${(mb / 1024).toLocaleString(undefined, { maximumFractionDigits: 1 })} GB`;
  const placeableCores = $derived(shape.cpus - reserveCores);
  const placeableMb = $derived(shape.memoryMb - reserveMb);

  /* -- the reserve --------------------------------------------------------------- */

  const coreNotches = $derived(
    shape.cpus > 1 ? Array.from({ length: shape.cpus }, (_, i) => i) : [0],
  );
  const coreMarks = $derived.by(() => {
    const marks: { value: number; label: string; recommended?: boolean }[] = [
      { value: 0, label: 'none' },
    ];
    if (recCores > 0) marks.push({ value: recCores, label: 'recommended', recommended: true });
    const half = Math.floor(shape.cpus / 2);
    if (half > recCores && half < shape.cpus - 1)
      marks.push({ value: half, label: 'half the machine' });
    const top = shape.cpus - 1;
    if (top > 0 && !marks.some((m) => m.value === top))
      marks.push({ value: top, label: `${top} of ${shape.cpus}` });
    return marks;
  });
  /* A figure typed off the notches joins them, so the slider holds it. */
  const mbNotches = $derived(withValue(memoryNotches(shape.memoryMb), reserveMb));
  const mbMarks = $derived.by(() => {
    const marks: { value: number; label: string; recommended?: boolean }[] = [
      { value: 0, label: 'none' },
    ];
    if (recMb > 0 && mbNotches.includes(recMb))
      marks.push({ value: recMb, label: 'recommended', recommended: true });
    const top = mbNotches[mbNotches.length - 1] ?? 0;
    if (top > 0 && top !== recMb) marks.push({ value: top, label: memoryLabel(top) });
    return marks;
  });
  const diskNotchList = $derived(withValue(diskNotches(diskTotalMb), reserveDiskMb));
  const diskMarks = $derived.by(() => {
    const marks: { value: number; label: string; recommended?: boolean }[] = [
      { value: 0, label: 'none' },
    ];
    if (recDiskMb > 0 && diskNotchList.includes(recDiskMb))
      marks.push({ value: recDiskMb, label: 'recommended', recommended: true });
    const top = diskNotchList[diskNotchList.length - 1] ?? 0;
    if (top > 0 && top !== recDiskMb) marks.push({ value: top, label: memoryLabel(top) });
    return marks;
  });
  /* The reserve is the memory pressure line as well as a placement charge:
     the controller holds new starts and steps a host down its throttle ladder
     once available memory falls to it, with a 512 MB floor under whatever the
     operator set. An operator raising it to protect the machine is moving the
     line they will then be throttled at, and nothing else on the page says
     so. */
  const memoryFloorMb = $derived(Math.max(reserveMb, 512));
  const pressureAtPercent = $derived(
    shape.memoryMb > 0 ? Math.round(((shape.memoryMb - memoryFloorMb) / shape.memoryMb) * 100) : 0,
  );

  const aboveCores = $derived(recCores > 0 && reserveCores > recCores);
  const aboveMb = $derived(recMb > 0 && reserveMb > recMb);
  const aboveDisk = $derived(recDiskMb > 0 && reserveDiskMb > recDiskMb);
  const placeableDiskMb = $derived(diskTotalMb - reserveDiskMb);

  const atRecommendation = $derived(
    parsed === recCapacity &&
      reserveCores === recCores &&
      reserveMb === recMb &&
      reserveDiskMb === recDiskMb,
  );
  function recommend(): void {
    // The reserve first, then the capacity that follows from it, so the
    // number set is the one the sentence beneath the slider explains.
    reserveCores = recCores;
    reserveMb = recMb;
    reserveDiskMb = recDiskMb;
    capacity = String(recommendedCapacity(shape, recCores, recMb, ask));
  }

  function close(): void {
    open = false;
    onclose?.();
  }

  async function save(confirm = false): Promise<void> {
    if (!host?.id || capacityError) return;
    saving = true;
    errors = {};
    try {
      await updateHost(
        host.id,
        {
          capacity: parsed,
          // Only what this host can honour: a field it has never reported is
          // left alone rather than sent as a zero it would have to refuse.
          ...(shape.cpus > 0 ? { reserve_cpus: reserveCores } : {}),
          ...(shape.memoryMb > 0 ? { reserve_memory_mb: reserveMb } : {}),
          ...(diskTotalMb > 0 ? { reserve_disk_mb: reserveDiskMb } : {}),
        },
        confirm ? { confirm: true } : undefined,
      );
      await fleet.reconcile();
      toasts.success(
        `${host.name || host.id} adjusted`,
        `${pluralise(parsed, 'runner slot')}${sized ? `, ${reserveCores === 0 && reserveMb === 0 ? 'nothing held back beyond the floors' : `${pluralise(reserveCores, 'core')} and ${memoryLabel(reserveMb)} held back`}` : ''}. The scheduler uses it on its next pass.`,
      );
      close();
    } catch (cause) {
      // The stranding refusal is a question, not a failure: it is answered in
      // the dialog, so it does not become a toast that closes over the form
      // the answer is in.
      if (cause instanceof ApiError && cause.isConflict) {
        stranding = cause.message;
        return;
      }
      if (cause instanceof ApiError) errors = cause.fieldErrors();
      toasts.fromError(cause, 'That host was not adjusted');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  title="Adjust {host?.name || 'host'}"
  description="How many runners it may hold, and what it keeps for itself. The scheduler uses it on its next pass."
  onclose={close}
>
  <form
    id="host-capacity-form"
    class="form"
    onsubmit={(event) => {
      event.preventDefault();
      void save();
    }}
  >
    <div class="recommend">
      <p>
        {#if sized && recCapacity > 0}
          A runner here asks for <strong>{ask.cpus} {ask.cpus === 1 ? 'core' : 'cores'}</strong>
          and <strong>{gb(ask.memoryMb)}</strong>{ask.source === 'pools'
            ? ask.pool
              ? `, the largest ask across your enabled pools — ${ask.pool}'s`
              : ', the largest ask across your enabled pools'
            : ', the default a new pool gets'}{ask.pair
            ? ', counting both containers of a docker-in-docker slot — its runner and the daemon, which is given the same limits because the build runs there'
            : ''}. On {shape.cpus > 0 ? `${shape.cpus} cores` : 'an unknown number of cores'}
          and {shape.memoryMb > 0 ? gb(shape.memoryMb) : 'unknown memory'}, that is room for
          <strong>{pluralise(recCapacity, 'runner')}</strong> once the reserve is kept.
        {:else}
          This host has not said how big it is, so there is no recommendation to make. An agent on
          the current release reports its cores and memory on its next heartbeat.
        {/if}
      </p>
      {#if sized && recCapacity > 0}
        <Button
          variant="secondary"
          size="sm"
          icon={Sparkles}
          disabled={atRecommendation}
          onclick={recommend}
        >
          Set to recommendations
        </Button>
      {/if}
    </div>

    <Field
      label="Maximum runners on this host"
      error={capacityError}
      hint="It is running {pluralise(
        host?.active_runners ?? 0,
        'runner',
      )} now. Lowering this does not interrupt them; it only stops new placement until there is room."
    >
      {#snippet children({ id, describedBy, invalid })}
        <div class="slots">
          <Slider
            values={capacityNotches}
            value={Number.isFinite(parsed) ? parsed : 0}
            label="Runner slots"
            valuetext={(v) => (v === 0 ? 'Paused' : pluralise(v, 'runner'))}
            marks={capacityMarks}
            tone={aboveCapacity ? 'warning' : 'accent'}
            readout={false}
            {describedBy}
            onchange={(v) => (capacity = String(v))}
          />
          <Input
            bind:value={capacity}
            {id}
            {describedBy}
            {invalid}
            type="number"
            min={0}
            step={1}
            size="sm"
            mono
          />
        </div>
      {/snippet}
    </Field>
    {#if hasStandard}
      <p class="note" data-testid="capacity-standard-note">
        This host has a standard runner size of {standardText}, so it takes as many runners as its
        machine holds of that size — {pluralise(host?.slots ?? 0, 'runner')} now — and this figure is
        only the most it may take. Change the size under “Set runner sizes” on its card.
      </p>
    {/if}
    {#if aboveCapacity}
      <div class="callout" role="status">
        <TriangleAlert size={16} aria-hidden="true" />
        <div>
          <p class="title">Above the recommendation</p>
          <p>
            {pluralise(parsed, 'runner')} would ask for {parsed * ask.cpus} cores and {gb(
              parsed * ask.memoryMb,
            )} on a host with {placeableCores} cores and {gb(placeableMb)} to place on{over.cpus >
              0 || over.memoryMb > 0
              ? `: ${[over.cpus > 0 ? `${over.cpus} cores` : '', over.memoryMb > 0 ? gb(over.memoryMb) : ''].filter(Boolean).join(' and ')} more than there is`
              : ''}. The scheduler still places only what fits, so the extra slots read as free
            while jobs wait on them; and a runner without a CPU limit can push the host past what a
            throttle can calm.
          </p>
        </div>
      </div>
    {/if}

    {#if sized}
      <fieldset class="reserve">
        <legend>Held back for the machine</legend>
        <p class="note">
          What the scheduler will not promise away: the room this host needs to be a working machine
          rather than a pool of capacity. It is arithmetic rather than a fence — the room is kept by
          placing less here, and nothing stops a job that runs away from taking it. A floor applies
          even at none: half a core or a twentieth of the machine, whichever is larger, 512 MB of
          memory and 2 GB of disk.
        </p>
        {#if shape.cpus > 1}
          <Field
            label="Cores"
            hint="Of {shape.cpus} on this host."
            error={errors.reserve_cpus ?? ''}
          >
            {#snippet children({ id, describedBy })}
              <QuantityField
                {id}
                quantity="cpus"
                whole
                values={coreNotches}
                value={reserveCores}
                label="Cores held back"
                valuetext={(v) => (v === 0 ? 'None' : pluralise(v, 'core'))}
                marks={coreMarks}
                tone={aboveCores ? 'warning' : 'accent'}
                empty={{ value: 0, placeholder: 'none' }}
                {describedBy}
                onchange={(v) => (reserveCores = v ?? 0)}
              />
            {/snippet}
          </Field>
          {#if aboveCores}
            <div class="callout" role="status">
              <TriangleAlert size={16} aria-hidden="true" />
              <div>
                <p class="title">More cores held back than recommended</p>
                <p>
                  {pluralise(placeableCores, 'core')} left for runners, which is room for about {pluralise(
                    Math.max(0, Math.floor(placeableCores / ask.cpus)),
                    'runner',
                  )} at {ask.cpus} each. The recommendation is {pluralise(recCores, 'core')}: enough
                  for the daemon, the agent and the kernel on a machine this size.
                </p>
              </div>
            </div>
          {/if}
        {/if}
        {#if shape.memoryMb > 1024}
          <Field
            label="Memory"
            hint="Of {gb(shape.memoryMb)} on this host."
            error={errors.reserve_memory_mb ?? ''}
          >
            {#snippet children({ id, describedBy })}
              <QuantityField
                {id}
                quantity="mb"
                values={mbNotches}
                value={reserveMb}
                label="Memory held back"
                valuetext={(v) => (v === 0 ? 'None' : memoryLabel(v))}
                marks={mbMarks}
                tone={aboveMb ? 'warning' : 'accent'}
                empty={{ value: 0, placeholder: 'none' }}
                {describedBy}
                onchange={(v) => (reserveMb = v ?? 0)}
              />
            {/snippet}
          </Field>
          {#if aboveMb}
            <div class="callout" role="status">
              <TriangleAlert size={16} aria-hidden="true" />
              <div>
                <p class="title">More memory held back than recommended</p>
                <p>
                  {gb(placeableMb)} left for runners, which is room for about {pluralise(
                    Math.max(0, Math.floor(placeableMb / ask.memoryMb)),
                    'runner',
                  )} at {gb(ask.memoryMb)} each. The recommendation is {memoryLabel(recMb)}: a tenth
                  of the machine, for the page cache and the daemon.
                </p>
              </div>
            </div>
          {/if}
          <p class="note">
            This figure is also the line pressure is judged against: when the host's available
            memory falls to its reserve it takes no new runners, and it starts stepping down its
            slots. Holding back more raises that line: with {memoryLabel(memoryFloorMb)} held back of
            {gb(shape.memoryMb)}, this host counts as under pressure once it is {pressureAtPercent}%
            full.
          </p>
        {/if}
        {#if diskNotchList.length > 1}
          <Field
            label="Disk"
            hint="Of {gb(diskTotalMb)} on the work directory's filesystem."
            error={errors.reserve_disk_mb ?? ''}
          >
            {#snippet children({ id, describedBy })}
              <QuantityField
                {id}
                quantity="mb"
                values={diskNotchList}
                value={reserveDiskMb}
                label="Disk held back"
                valuetext={(v) => (v === 0 ? 'None' : memoryLabel(v))}
                marks={diskMarks}
                tone={aboveDisk ? 'warning' : 'accent'}
                empty={{ value: 0, placeholder: 'none' }}
                {describedBy}
                onchange={(v) => (reserveDiskMb = v ?? 0)}
              />
            {/snippet}
          </Field>
          {#if aboveDisk}
            <div class="callout" role="status">
              <TriangleAlert size={16} aria-hidden="true" />
              <div>
                <p class="title">More disk held back than recommended</p>
                <p>
                  {gb(placeableDiskMb)} left for the checkouts and caches every runner here writes. The
                  recommendation is {memoryLabel(recDiskMb)}: a tenth of the filesystem, so the
                  machine keeps working when a job fills its share.
                </p>
              </div>
            </div>
          {/if}
        {/if}
      </fieldset>
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
        form="host-capacity-form"
        loading={saving}
        disabled={Boolean(capacityError)}
      >
        Save changes
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
  .recommend {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-4);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-accent-border);
    border-radius: var(--z-radius-md);
    background: var(--z-accent-subtle);
  }
  .recommend p {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .recommend strong {
    color: var(--z-text);
    font-weight: var(--z-weight-semibold);
    font-variant-numeric: tabular-nums;
  }
  /* The exact number beside the slider, for the operator who knows the
     figure: the two are one setting and move together. */
  .slots {
    display: grid;
    grid-template-columns: 1fr 6rem;
    align-items: start;
    gap: var(--z-space-4);
  }
  .callout {
    display: flex;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
    color: var(--z-pending);
  }
  .callout p {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .callout .title {
    color: var(--z-pending);
    font-weight: var(--z-weight-semibold);
    margin-bottom: var(--z-nudge-2);
  }
  .callout :global(svg) {
    flex: none;
    margin-top: var(--z-nudge-2);
  }
  /* The controller's refusal, which is not the same thing as a setting past
     its recommendation: nothing was saved, so it is coloured danger. */
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
  .reserve {
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
  .note {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }
  @media (max-width: 480px) {
    .recommend {
      flex-direction: column;
    }
    .slots {
      grid-template-columns: 1fr;
    }
  }
</style>

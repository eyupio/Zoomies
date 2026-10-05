<!--
  How many runners, for how long, and the timings of a runner's own life.

  It comes after the size on purpose. A maximum is a number about machines --
  it means something only once it is known what one runner costs and what the
  hosts can hold -- and asked first it was a number typed into an empty box
  with nothing on screen able to say what it would buy. By the time this is
  read the fleet has an answer, so the maximum starts at what the hosts can
  actually place and says where the figure came from.

  It stays a figure rather than becoming "as many as fit". The maximum is a
  backstop: it is what stops one misconfigured workflow, or one repository
  under a fork-pull-request storm, filling every machine and every rented one
  behind it. A cap that silently grew with the fleet would be a cap nobody
  chose, so the fleet's room is offered and an operator accepts it.

  The timings are the exception that is kept out of the way. Every one is empty
  by default, and empty is the answer that means something: the pool follows the
  fleet, and keeps following it when the fleet's figure is changed on the
  Settings page. A form that pre-filled them with the fleet's values would look
  identical and behave differently -- every pool frozen on whatever the fleet
  meant the day it was made -- so the figures are shown as placeholders, where
  they can be read but not accidentally adopted. They answer a different
  question from the count: how long each step of a runner's life may take, which
  an operator sets from the shape of their images, and the pool that needs them
  is usually the one pulling twelve gigabytes of Windows.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { Sparkles, TriangleAlert } from '@lucide/svelte';
  import { parseGoDuration, pluralise, shortGoDuration } from '$lib/format';
  import type { PoolRoom as PoolRoomShape, Result } from '$lib/api/types';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import PoolMore from './PoolMore.svelte';
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
     * Whether the maximum is still the one the editor worked out from the
     * fleet. It stops following the moment an operator types their own.
     */
    following?: boolean;
    /** What the fleet answers for each timing, for the placeholders. */
    fleetDefaults: Result<'getPoolDefaults'>['runner_settings'] | null;
  }

  let {
    draft,
    errors,
    touch,
    verdict,
    validating,
    following = false,
    fleetDefaults,
  }: Props = $props();

  /* -- how many ---------------------------------------------------------------- */

  const timeout = $derived(parseGoDuration(draft.idle_timeout));
  // The way an operator says it: the display form of "5m" reads "5m 00s".
  const timeoutText = $derived(timeout === null ? '' : shortGoDuration(draft.idle_timeout));

  // A pool that takes its size from each host has none of its own to count
  // runners of.
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

  /* -- the timings -------------------------------------------------------------- */

  type Key =
    | 'provision_timeout'
    | 'drain_timeout'
    | 'max_runner_lifetime'
    | 'scale_up_delay'
    | 'docker_wait';

  interface Setting {
    key: Key;
    label: string;
    hint: string;
    /** What a zero means here, which is never "unset" and never the same twice. */
    zero: string;
    /** True for a setting that only applies to a pool giving its jobs Docker. */
    needsDaemon?: boolean;
  }

  const SETTINGS: readonly Setting[] = [
    {
      key: 'provision_timeout',
      label: 'Provision timeout',
      hint: 'How long a runner of this pool may take to register before it is given up on. Raise it for a pool with a large image or a slow registry.',
      zero: 'never give up on a runner that is still starting',
    },
    {
      key: 'drain_timeout',
      label: 'Drain timeout',
      hint: 'How long a runner may sit draining with no job left on it before its slot is taken back. A runner still finishing a job is never touched by this.',
      zero: 'leave a drain unbounded',
    },
    {
      key: 'max_runner_lifetime',
      label: 'Maximum runner lifetime',
      hint: 'A runner this old is drained the next time it is not busy. It never ends a job, so it is no answer to a hung one.',
      zero: 'let a runner live until something else removes it',
    },
    {
      key: 'scale_up_delay',
      label: 'Scale-up delay',
      hint: 'How long a job for this pool must have been queued before it counts as demand. Lower it for a pool whose jobs are short.',
      zero: 'scale the moment a job is queued',
    },
    {
      key: 'docker_wait',
      label: 'Docker wait',
      hint: 'How long a runner waits for the Docker daemon it was promised before refusing a job. Only a pool that gives its jobs a daemon does this wait.',
      zero: "leave the runner image's own wait in place",
      needsDaemon: true,
    },
  ];

  const givesDaemon = $derived(draft.docker_mode !== 'none');
  const overridden = $derived(SETTINGS.filter((s) => draft[s.key].trim() !== '').length);

  function placeholder(setting: Setting): string {
    const value = fleetDefaults?.[setting.key];
    return value ? `${value} — this fleet's` : "this fleet's";
  }

  /*
    The one relationship between these settings that is worth catching before
    the server does.

    A provision timeout inside the time a runner legitimately takes to start --
    the agent's create budget plus this pool's own Docker wait -- fails runners
    that are still coming up, and the replacement pulls the same image over the
    link that was slow to begin with. The fleet's own defaults are held out of
    that order by a test; a pool that overrides either half is not, which is
    exactly what this panel makes possible.

    It is a caution rather than an error: the server raises the same thing as a
    pool warning, and there are fleets whose images are already on every host
    where a shorter timeout is a reasonable thing to want. Saying it here means
    it is read while the number is being chosen rather than afterwards.
  */
  const CREATE_BUDGET_MINUTES = 15;

  function minutes(value: string): number | null {
    const text = value.trim();
    if (text === '') return null;
    const match = /^(\d+(?:\.\d+)?)(ms|s|m|h)$/.exec(text);
    if (!match) return null;
    const amount = Number(match[1]);
    switch (match[2]) {
      case 'ms':
        return amount / 60000;
      case 's':
        return amount / 60;
      case 'h':
        return amount * 60;
      default:
        return amount;
    }
  }

  const ladder = $derived.by(() => {
    const limit = minutes(draft.provision_timeout);
    // Nothing typed follows the fleet, whose own order is already held; zero
    // is "never give up", which cannot condemn anything.
    if (limit === null || limit === 0) return '';
    const wait = givesDaemon ? (minutes(draft.docker_wait) ?? 2) : 0;
    const start = CREATE_BUDGET_MINUTES + wait;
    if (limit > start) return '';
    const because = givesDaemon
      ? `${CREATE_BUDGET_MINUTES}m for the create and ${wait}m waiting for Docker`
      : `${CREATE_BUDGET_MINUTES}m for the create`;
    return `A runner of this pool may legitimately take ${start}m to start — ${because} — so a ${draft.provision_timeout.trim()} timeout fails runners that are still coming up, and the replacement pulls the same image again.`;
  });

  // Open already when something in it is in use, or refused: a setting that is
  // on is never hidden behind a closed row.
  const timingErrors = SETTINGS.map((s) => `runner_settings.${s.key}`);
  let moreOpen = $state(
    untrack(
      () =>
        overridden > 0 ||
        Number(draft.priority) !== 0 ||
        Boolean(errors['priority']) ||
        timingErrors.some((field) => Boolean(errors[field])),
    ),
  );
  const moreNote = $derived(
    [
      Number(draft.priority) !== 0 ? `priority ${Number(draft.priority)}` : '',
      overridden > 0
        ? `${overridden} timing ${overridden === 1 ? 'override' : 'overrides'}`
        : 'every timing follows the fleet',
    ]
      .filter(Boolean)
      .join(' · '),
  );
</script>

<div class="pair">
  <Field
    label="Minimum runners"
    error={errors['min_runners']}
    hint="Kept warm even when nothing is queued. Zero costs nothing while idle, at the price of a cold start on the first job."
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
    hint="The backstop: however many jobs GitHub queues, this pool never creates more runners than this."
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
  The same count the size section shows, read the other way round: there it says
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
  <div class="lead">
    <p class="echo">
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

<PoolMore title="Priority and runner timings" note={moreNote} bind:open={moreOpen}>
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

  <p class="echo">
    Every runner timing has a fleet-wide answer on the Settings page, and this pool follows it
    unless you say otherwise here. Leave a field empty to keep following — including after the
    fleet's own figure is changed.
  </p>

  <div class="settings">
    {#each SETTINGS as setting (setting.key)}
      {@const inactive = setting.needsDaemon === true && !givesDaemon}
      <Field
        label={setting.label}
        error={errors[`runner_settings.${setting.key}`]}
        hint={inactive
          ? `${setting.hint} This pool's jobs get no daemon, so it changes nothing here.`
          : `${setting.hint} 0 means: ${setting.zero}.`}
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={draft[setting.key]}
            {id}
            {describedBy}
            {invalid}
            disabled={inactive}
            placeholder={placeholder(setting)}
            autocomplete="off"
            spellcheck={false}
            onblur={() => touch(`runner_settings.${setting.key}`)}
          />
        {/snippet}
      </Field>
    {/each}
  </div>

  {#if ladder}
    <div class="callout" role="status">
      <TriangleAlert size={16} aria-hidden="true" />
      <div>
        <p class="callout-title">Shorter than a runner of this pool takes to start</p>
        <p>{ladder}</p>
      </div>
    </div>
  {/if}

  <p class="echo">
    {#if overridden === 0}
      This pool follows the fleet on every runner timing.
    {:else}
      This pool overrides {overridden} of {SETTINGS.length} runner timings; the rest follow the fleet.
    {/if}
  </p>
</PoolMore>

<style>
  .settings {
    display: grid;
    gap: var(--z-space-4);
  }
</style>

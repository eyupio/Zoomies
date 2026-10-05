<!--
  What an operator may say about a pool the controller keeps.

  Four things, and only four: how many runners to keep ready, a cap, how long an
  idle runner lives, and whether the memory valve may lend a runner more memory.
  Everything else about the pool -- its labels, its size, its minimum and
  maximum -- follows the hosts it is kept for, so the controller would put it
  back on its next pass, and the API refuses to take a change it would undo.
  Saying so here, instead of showing the editor and letting it be refused, is the
  whole of this dialog.

  The valve is a policy and not a figure worked out from the hosts, which is why
  it is the operator's: the reconciler writes no column it lives in, and without
  this the pools most fleets run on would be the ones that could never turn it on.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { ApiError, updatePool } from '$lib/api/client';
  import type { Body, Pool } from '$lib/api/types';
  import { formatGoDuration, parseGoDuration } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import { memoryBurstErrors } from './draft';
  import ElasticMemory from './ElasticMemory.svelte';

  interface Props {
    open?: boolean;
    pool: Pool | null;
    onclose?: () => void;
  }

  let { open = $bindable(false), pool, onclose }: Props = $props();

  let warm = $state('');
  let cap = $state('');
  let idle = $state('');
  // The valve's three fields, in the shape the editor's control reads and writes.
  let memory = $state({
    memory_burst_mode: 'off' as 'off' | 'observe' | 'automatic',
    memory_burst_max: '',
    memory_burst_spill: '',
  });
  // What the valve was when the dialog opened, to send it only if it was changed:
  // a save that says nothing about memory must not overwrite a policy changed
  // since, and a PATCH carries what the operator did.
  let memoryLoaded = $state('');
  let saving = $state(false);
  let errors = $state<Record<string, string>>({});
  let loadedFor = $state<string | null>(null);

  // Reloaded when a different pool is opened and only then, so an SSE frame for
  // the pool does not overwrite what is being typed.
  $effect(() => {
    if (!open || !pool) {
      loadedFor = null;
      return;
    }
    if (loadedFor === pool.id) return;
    loadedFor = pool.id ?? null;
    warm = String(pool.auto?.warm ?? 0);
    cap = String(pool.auto?.cap ?? 0);
    idle = pool.idle_timeout ?? '';
    memory = {
      memory_burst_mode: pool.memory_burst?.mode ?? 'off',
      memory_burst_max: pool.memory_burst?.max_memory_mb
        ? String(pool.memory_burst.max_memory_mb)
        : '',
      memory_burst_spill: pool.memory_burst?.spill_mb ? String(pool.memory_burst.spill_mb) : '',
    };
    memoryLoaded = JSON.stringify(memory);
    errors = {};
  });

  // A refusal is about the figure that was sent, and it disables Save, so it has
  // to go when that figure is edited. Each field withdraws only its own, and
  // errors is read untracked so a refusal that has just arrived is not cleared
  // by the effect it did not come from.
  $effect(() => {
    void warm;
    untrack(() => {
      if (errors['auto.warm']) delete errors['auto.warm'];
    });
  });
  $effect(() => {
    void cap;
    untrack(() => {
      if (errors['auto.cap']) delete errors['auto.cap'];
    });
  });
  $effect(() => {
    void idle;
    untrack(() => {
      if (errors['idle_timeout']) delete errors['idle_timeout'];
    });
  });

  const whole = (text: string): boolean => {
    const n = Number(text);
    return text.trim() !== '' && Number.isInteger(n) && n >= 0;
  };
  const warmError = $derived(
    errors['auto.warm'] ?? (whole(warm) ? '' : 'Use a whole number of zero or more.'),
  );
  const capError = $derived(
    errors['auto.cap'] ?? (whole(cap) ? '' : 'Use a whole number of zero or more.'),
  );
  const idleError = $derived(
    errors['idle_timeout'] ??
      ((parseGoDuration(idle) ?? 0) > 0
        ? ''
        : 'Use a duration such as 5m, 90s or 1h30m, longer than zero.'),
  );
  // The same rules the editor holds the valve to, with no size to compare a ceiling
  // against: a pool the controller keeps takes its size from its hosts.
  const memoryErrors = $derived({
    ...memoryBurstErrors({
      ...memory,
      backend: pool?.backend ?? 'docker',
      sizing: 'profile',
      memory_mb: '',
      docker_mode: pool?.docker_mode ?? 'none',
    }),
    ...Object.fromEntries(Object.entries(errors).filter(([key]) => key.startsWith('memory_burst'))),
  });
  const anyError = $derived(
    Boolean(warmError || capError || idleError || Object.keys(memoryErrors).length > 0),
  );
  const maximum = $derived(pool?.max_runners ?? 0);

  function close(): void {
    open = false;
    onclose?.();
  }

  async function save(): Promise<void> {
    if (!pool?.id || anyError) return;
    saving = true;
    errors = {};
    try {
      const body: Body<'updatePool'> = {
        idle_timeout: idle.trim(),
        auto: { warm: Number(warm), cap: Number(cap) },
        // Sent only if it was changed, for a pool that has a container to raise
        // the limit of, and with no figures where the valve is off, which the
        // server refuses.
        ...(pool.backend === 'process' || JSON.stringify(memory) === memoryLoaded
          ? {}
          : {
              memory_burst: {
                mode: memory.memory_burst_mode,
                max_memory_mb:
                  memory.memory_burst_mode === 'off' ? 0 : Number(memory.memory_burst_max) || 0,
                spill_mb:
                  memory.memory_burst_mode === 'off' ? 0 : Number(memory.memory_burst_spill) || 0,
              },
            }),
      };
      await updatePool(pool.id, body);
      await fleet.reconcile();
      toasts.success(
        `${pool.name || 'Pool'} settings saved`,
        'Its maximum still follows its hosts. The controller applies these on its next pass.',
      );
      close();
    } catch (cause) {
      if (cause instanceof ApiError) errors = cause.fieldErrors();
      toasts.fromError(cause, 'Those settings were not saved');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  size="sm"
  title="Settings for {pool?.name || 'this pool'}"
  description="The controller keeps this pool for its hosts. These are the four things it leaves to you."
  onclose={close}
>
  <form
    id="pool-auto-form"
    class="fields"
    onsubmit={(event) => {
      event.preventDefault();
      void save();
    }}
  >
    <Field
      label="Runners to keep ready"
      error={warmError}
      hint="Idle runners kept started so a job does not wait for one. Never more than the pool's maximum, which is {maximum} now. Use 0 to keep none."
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input bind:value={warm} {id} {describedBy} {invalid} type="number" min={0} step={1} mono />
      {/snippet}
    </Field>
    <Field
      label="Cap"
      error={capError}
      hint="The most runners this pool may have, however many its hosts hold. Use 0 for no cap."
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input bind:value={cap} {id} {describedBy} {invalid} type="number" min={0} step={1} mono />
      {/snippet}
    </Field>
    <Field
      label="Idle timeout"
      error={idleError}
      hint="How long a runner above the ones kept ready waits for work before it is destroyed. A Go duration: 5m, 90s, 1h30m."
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={idle}
          {id}
          {describedBy}
          {invalid}
          mono
          placeholder="5m"
          autocomplete="off"
        />
      {/snippet}
    </Field>
    {#if parseGoDuration(idle)}
      <p class="echo">
        Runners above the ones kept ready are destroyed after {formatGoDuration(idle)} with no work.
      </p>
    {/if}
    {#if pool?.backend !== 'process'}
      <ElasticMemory draft={memory} errors={memoryErrors} touch={() => {}} room={null} />
    {/if}
  </form>

  {#snippet footer()}
    <Button variant="ghost" onclick={close}>Cancel</Button>
    <Button
      variant="primary"
      type="submit"
      form="pool-auto-form"
      loading={saving}
      disabled={anyError}
    >
      Save settings
    </Button>
  {/snippet}
</Dialog>

<style>
  .fields {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-bottom: var(--z-space-2);
  }
  /* ElasticMemory is the pool editor's control, and like every control there it
     takes its form vocabulary from whatever holds it: PoolSection's body in the
     editor, this form here. Without it the fieldset is the browser's -- a groove
     drawn round it, and a width that will not shrink below its content, which on
     a phone is wider than the dialog -- and its hints are body text. */
  .fields :global(.group) {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    min-width: 0;
    margin: 0;
    padding: 0;
    border: 0;
  }
  /* The legend is not a flex item, so the group's gap does not reach it. It also
     carries the rule above the group, as PoolSection's does between two, because
     a fieldset's own border is broken where its legend sits. */
  .fields :global(.group > legend) {
    width: 100%;
    padding: var(--z-space-5) 0 var(--z-space-4);
    border-top: var(--z-border-width) solid var(--z-border);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .fields :global(.hint),
  .fields :global(.echo) {
    margin: 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    overflow-wrap: anywhere;
  }
  .fields :global(.hint) {
    color: var(--z-text-subtle);
  }
  .fields :global(.echo) {
    color: var(--z-text-muted);
  }
</style>

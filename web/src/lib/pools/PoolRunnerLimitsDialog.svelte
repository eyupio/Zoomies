<!-- Fast path for scaling a pool without walking through its configuration wizard. -->
<script lang="ts">
  import { untrack } from 'svelte';
  import { ApiError, updatePool } from '$lib/api/client';
  import type { Body, Pool } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';

  interface Props {
    open?: boolean;
    pool: Pool | null;
    onclose?: () => void;
  }

  let { open = $bindable(false), pool, onclose }: Props = $props();
  let minimum = $state('');
  let maximum = $state('');
  let saving = $state(false);
  let errors = $state<Record<string, string>>({});
  let loadedFor = $state<string | null>(null);

  $effect(() => {
    if (!open || !pool) {
      loadedFor = null;
      return;
    }
    if (loadedFor === pool.id) return;
    loadedFor = pool.id ?? null;
    minimum = String(pool.min_runners ?? 0);
    maximum = String(pool.max_runners ?? 1);
    errors = {};
  });

  // A refusal is about the figure that was sent, and it also disables Save, so
  // it has to go when that figure is edited or the form cannot be resubmitted.
  // Each figure withdraws only its own, and errors is read untracked so a
  // refusal that has just arrived is not cleared by the effect it did not come
  // from.
  $effect(() => {
    void minimum;
    untrack(() => {
      if (errors.min_runners) delete errors.min_runners;
    });
  });
  $effect(() => {
    void maximum;
    untrack(() => {
      if (errors.max_runners) delete errors.max_runners;
    });
  });

  const min = $derived(Number(minimum));
  const max = $derived(Number(maximum));
  const minError = $derived(
    errors.min_runners ??
      (minimum.trim() === '' || !Number.isInteger(min) || min < 0
        ? 'The minimum must be a whole number of zero or more.'
        : ''),
  );
  const maxError = $derived(
    errors.max_runners ??
      (maximum.trim() === '' || !Number.isInteger(max) || max < 1
        ? 'The maximum must be a whole number of one or more.'
        : max < min
          ? `The maximum must be at least the minimum, which is ${min}.`
          : ''),
  );
  const anyError = $derived(Boolean(minError || maxError));

  function close(): void {
    open = false;
    onclose?.();
  }

  async function save(): Promise<void> {
    if (!pool?.id || anyError) return;
    saving = true;
    errors = {};
    try {
      const body: Body<'updatePool'> = { min_runners: min, max_runners: max };
      await updatePool(pool.id, body);
      await fleet.reconcile();
      toasts.success(`${pool.name || 'Pool'} runner limits set to ${min}–${max}`);
      close();
    } catch (cause) {
      if (cause instanceof ApiError) errors = cause.fieldErrors();
      toasts.fromError(cause, 'Those runner limits were not changed');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  size="sm"
  title="Runner limits"
  description="Change only how far {pool?.name || 'this pool'} may scale."
  onclose={close}
>
  <form
    id="pool-runner-limits-form"
    class="fields"
    onsubmit={(event) => {
      event.preventDefault();
      void save();
    }}
  >
    <Field
      label="Minimum runners"
      error={minError}
      hint="Warm runners kept ready even when no job is queued. Use 0 for scale-to-zero."
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={minimum}
          {id}
          {describedBy}
          {invalid}
          type="number"
          min={0}
          step={1}
          mono
        />
      {/snippet}
    </Field>
    <Field
      label="Maximum runners"
      error={maxError}
      hint="The hard ceiling for this pool. Host capacities still apply."
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={maximum}
          {id}
          {describedBy}
          {invalid}
          type="number"
          min={1}
          step={1}
          mono
        />
      {/snippet}
    </Field>
  </form>

  {#snippet footer()}
    <Button variant="ghost" onclick={close}>Cancel</Button>
    <Button
      variant="primary"
      type="submit"
      form="pool-runner-limits-form"
      loading={saving}
      disabled={anyError}
    >
      Save limits
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
</style>

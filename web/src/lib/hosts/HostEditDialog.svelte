<!--
  Capacity and labels for one host.

  Capacity is how many runners the scheduler may place here at once; the field
  says so, because the number on its own invites being set to the host's CPU
  count for reasons that are not quite right. Lowering it below what is already
  running is allowed and does not evict anything -- it simply stops more being
  placed -- and the hint says that too.
-->
<script lang="ts">
  import { updateHost } from '$lib/api/client';
  import { ApiError } from '$lib/api/client';
  import type { Host } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import LabelMapEditor from './LabelMapEditor.svelte';

  interface Props {
    open?: boolean;
    host: Host | null;
    onclose?: () => void;
  }

  let { open = $bindable(false), host, onclose }: Props = $props();

  let capacity = $state('');
  let rows = $state<{ key: string; value: string }[]>([]);
  let saving = $state(false);
  let errors = $state<Record<string, string>>({});

  // Reload the form whenever a different host is opened, and only then: an SSE
  // update to the host must not overwrite what is being typed.
  let loadedFor = $state<string | null>(null);
  $effect(() => {
    if (!open || !host) {
      loadedFor = null;
      return;
    }
    if (loadedFor === host.id) return;
    loadedFor = host.id ?? null;
    capacity = String(host.capacity ?? 0);
    rows = Object.entries(host.labels ?? {}).map(([key, value]) => ({ key, value }));
    errors = {};
  });

  const active = $derived(host?.active_runners ?? 0);
  const parsed = $derived(Number(capacity));
  const capacityError = $derived(
    capacity.trim() === ''
      ? 'Give a number. Use 0 to stop new runners being placed here at all.'
      : !Number.isInteger(parsed) || parsed < 0
        ? 'Capacity is a whole number of runners, and cannot be negative.'
        : '',
  );
  /** The server's field error wins, because it knows something we did not. */
  const capacityFieldError = $derived(errors.capacity ?? capacityError);

  async function save(): Promise<void> {
    if (!host?.id || capacityError) return;
    saving = true;
    errors = {};
    const labels: Record<string, string> = {};
    for (const row of rows) {
      const key = row.key.trim();
      if (key) labels[key] = row.value.trim();
    }
    try {
      await updateHost(host.id, { capacity: parsed, labels });
      await fleet.reconcile();
      toasts.success(`${host.name || host.id} updated`);
      open = false;
      onclose?.();
    } catch (cause) {
      if (cause instanceof ApiError) errors = cause.fieldErrors();
      toasts.fromError(cause, 'That host was not updated');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  title="Edit {host?.name || 'host'}"
  description="Capacity and labels take effect on the scheduler's next pass."
  {onclose}
>
  <div class="form">
    <Field
      label="Capacity"
      error={capacityFieldError}
      hint="How many runners may exist on this host at once. It is running {pluralise(
        active,
        'runner',
      )} now; setting a lower number does not stop them, it only prevents more."
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={capacity}
          {id}
          {describedBy}
          invalid={invalid || Boolean(capacityFieldError)}
          type="number"
          min={0}
          step={1}
        />
      {/snippet}
    </Field>

    <Field label="Labels" hint="Pools choose hosts by these key/value pairs.">
      {#snippet children({ describedBy })}
        <LabelMapEditor bind:rows {describedBy} />
      {/snippet}
    </Field>
  </div>

  {#snippet footer()}
    <Button
      variant="ghost"
      onclick={() => {
        open = false;
        onclose?.();
      }}
    >
      Cancel
    </Button>
    <Button variant="primary" loading={saving} disabled={Boolean(capacityError)} onclick={save}>
      Save changes
    </Button>
  {/snippet}
</Dialog>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-bottom: var(--z-space-2);
  }
</style>

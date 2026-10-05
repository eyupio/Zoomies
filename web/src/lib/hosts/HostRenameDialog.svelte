<!--
  A host's name.

  The name a host joined under is the agent's configured one, or, for a
  container deployment, whatever Docker called the first container. It is the
  word every other page uses for the machine, so it is the operator's to
  change. The agent's own name is only read when it first joins: renaming here
  is not undone by a heartbeat.
-->
<script lang="ts">
  import { ApiError, updateHost } from '$lib/api/client';
  import type { Host } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';

  interface Props {
    open?: boolean;
    host: Host | null;
    onclose?: () => void;
  }

  let { open = $bindable(false), host, onclose }: Props = $props();

  let name = $state('');
  let error = $state<string | undefined>(undefined);
  let saving = $state(false);

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
    name = host.name ?? '';
    error = undefined;
  });

  const trimmed = $derived(name.trim());
  const unchanged = $derived(trimmed === (host?.name ?? ''));

  function close(): void {
    open = false;
    onclose?.();
  }

  async function save(): Promise<void> {
    if (!host?.id || trimmed === '' || unchanged) return;
    saving = true;
    error = undefined;
    try {
      await updateHost(host.id, { name: trimmed });
      await fleet.reconcile();
      toasts.success(`Renamed to ${trimmed}`, `It was called ${host.name || host.id}.`);
      close();
    } catch (cause) {
      // A taken or malformed name is the one thing this form can be wrong
      // about, so it is said beside the field rather than only in a toast.
      if (cause instanceof ApiError) error = cause.fieldErrors().name;
      if (!error) toasts.fromError(cause, 'That host was not renamed');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  title="Rename {host?.name || 'host'}"
  description="The name is how this host appears on every page and in the CLI. Its runners and tags are not touched."
  onclose={close}
>
  <form
    onsubmit={(event) => {
      event.preventDefault();
      void save();
    }}
  >
    <Field label="Name" hint="Unique across the fleet, at most 128 characters." {error}>
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={name}
          {id}
          {describedBy}
          {invalid}
          autocomplete="off"
          spellcheck={false}
        />
      {/snippet}
    </Field>
  </form>

  {#snippet footer()}
    <Button variant="ghost" onclick={close}>Cancel</Button>
    <Button
      variant="primary"
      loading={saving}
      disabled={trimmed === '' || unchanged}
      onclick={save}
    >
      Rename
    </Button>
  {/snippet}
</Dialog>

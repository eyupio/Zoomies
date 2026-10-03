<script lang="ts">
  import {
    getContextInstallationOwners,
    listUsers,
    putContextInstallationOwners,
  } from '$lib/api/client';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  interface Props {
    open?: boolean;
    installation: { id: string; target: string } | null;
  }
  let { open = $bindable(false), installation }: Props = $props();
  let people = $state<{ id: string; label: string; username: string }[]>([]);
  let selected = $state<string[]>([]);
  let loading = $state(false);
  let saving = $state(false);
  let failure = $state<unknown>(null);
  let reload = $state(0);
  let ready = $state(false);

  $effect(() => {
    if (!open) {
      ready = false;
      return;
    }
    const id = installation?.id;
    if (!id) return;
    void reload;
    const controller = new AbortController();
    loading = true;
    failure = null;
    void Promise.all([
      listUsers(controller.signal),
      getContextInstallationOwners(id, controller.signal),
    ])
      .then(([users, owners]) => {
        if (controller.signal.aborted) return;
        people = (users.items ?? [])
          .filter((u) => u.id && u.username && !u.disabled)
          .map((u) => ({
            id: u.id as string,
            label: u.display_name || (u.username as string),
            username: u.username as string,
          }));
        selected = owners.user_ids;
        ready = true;
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) failure = cause;
      })
      .finally(() => {
        if (!controller.signal.aborted) loading = false;
      });
    return () => controller.abort();
  });

  async function save(): Promise<void> {
    const id = installation?.id;
    if (!id || saving || loading || failure) return;
    saving = true;
    try {
      await putContextInstallationOwners(id, selected);
      toasts.success(
        'Installation owners saved',
        `${selected.length} ${selected.length === 1 ? 'person' : 'people'} can now enable repositories for ${installation?.target}.`,
      );
      open = false;
    } catch (cause) {
      failure = cause;
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  title="Installation owners"
  description="Choose who may enable AI Context for {installation?.target ?? 'this installation'}."
  dismissible={!saving}
>
  <p class="guidance">
    Owners can prepare and set up repositories in this installation and add or remove only
    themselves as a source reader. Ownership does not grant source access to anyone.
  </p>
  {#if loading}
    <Skeleton lines={3} />
  {:else if failure}
    <ErrorState
      error={failure}
      title="Owners could not be loaded or saved"
      onretry={() => {
        reload += 1;
      }}
    />
  {:else}
    <p class="summary" aria-live="polite">{selected.length} owners selected</p>
    <div class="choices">
      {#each people as person (person.id)}
        <Checkbox
          label={person.label}
          description={person.username}
          checked={selected.includes(person.id)}
          disabled={saving || (selected.length >= 50 && !selected.includes(person.id))}
          onchange={(checked) => {
            selected = checked
              ? [...selected, person.id]
              : selected.filter((value) => value !== person.id);
          }}
        />
      {/each}
      {#if people.length === 0}<p class="guidance">There are no other people to choose.</p>{/if}
    </div>
  {/if}
  {#snippet footer()}
    <Button variant="secondary" disabled={saving} onclick={() => (open = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={saving}
      disabled={loading || !!failure || !ready}
      onclick={save}>Save owners</Button
    >
  {/snippet}
</Dialog>

<style>
  .guidance {
    color: var(--z-text-subtle);
    margin: 0 0 var(--z-space-4);
  }
  .summary {
    margin: 0 0 var(--z-space-4);
  }
  .choices {
    display: grid;
    gap: var(--z-space-4);
    overflow-wrap: anywhere;
  }
</style>

<script lang="ts">
  import { getOwnMCPContextSelection, putOwnMCPContextSelection } from '$lib/api/client';
  import type { MCPConnection } from '$lib/api/types';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  interface Props {
    open?: boolean;
    connection: MCPConnection | null;
  }
  let { open = $bindable(false), connection }: Props = $props();
  let items = $state<{ id: string; full_name: string }[]>([]);
  let selected = $state<string[]>([]);
  let publishing = $state<string[]>([]);
  let total = $state(0);
  let offset = $state(0);
  let loading = $state(false);
  let saving = $state(false);
  let failure = $state<unknown>(null);
  let reload = $state(0);
  let initialisedFor = $state('');

  $effect(() => {
    if (!open) {
      initialisedFor = '';
      offset = 0;
      return;
    }
    const id = connection?.id;
    if (!id) return;
    const page = offset;
    void reload;
    const controller = new AbortController();
    loading = true;
    failure = null;
    void getOwnMCPContextSelection(id, page, controller.signal)
      .then((result) => {
        if (controller.signal.aborted) return;
        items = result.items;
        total = result.total;
        if (initialisedFor !== id) {
          selected = result.selected_repository_ids;
          publishing = result.publish_repository_ids;
          initialisedFor = id;
        }
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        failure = cause;
      })
      .finally(() => {
        if (!controller.signal.aborted) loading = false;
      });
    return () => controller.abort();
  });

  function choose(id: string, checked: boolean): void {
    selected = checked ? [...selected, id] : selected.filter((value) => value !== id);
    // Publishing is a narrower grant inside reading, never without it.
    if (!checked) publishing = publishing.filter((value) => value !== id);
  }

  function allowPublish(id: string, checked: boolean): void {
    publishing = checked ? [...publishing, id] : publishing.filter((value) => value !== id);
  }

  async function save(): Promise<void> {
    const id = connection?.id;
    if (!id || saving || loading || failure) return;
    saving = true;
    try {
      await putOwnMCPContextSelection(id, selected, publishing);
      toasts.success(
        'Source access saved',
        `${connection?.client_name ?? 'This connection'} can read only the repositories you chose.`,
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
  title="Source access"
  description="Choose which repositories {connection?.client_name ??
    'this connection'} may read. Fleet permissions do not grant source access."
  dismissible={!saving}
>
  <p class="guidance">
    Only available repositories an administrator has explicitly shared with you appear here. No
    repository is selected automatically.
  </p>
  {#if loading}
    <Skeleton lines={3} />
  {:else if failure}
    <ErrorState
      error={failure}
      title="Source access could not be loaded or saved"
      onretry={() => {
        initialisedFor = '';
        reload += 1;
      }}
    />
  {:else if total === 0}
    <p class="empty">
      No source repositories are available to you yet. Ask an administrator to complete AI Context
      setup and add you as a reader.
    </p>
  {:else}
    <div class="selection-summary" aria-live="polite">
      <span>{selected.length} of 100 repositories selected</span>
      <Button
        size="sm"
        variant="ghost"
        disabled={selected.length === 0 || saving}
        onclick={() => {
          selected = [];
          publishing = [];
        }}>Remove all</Button
      >
    </div>
    <div class="choices">
      {#each items as item (item.id)}
        <Checkbox
          label={item.full_name}
          checked={selected.includes(item.id)}
          disabled={saving || (selected.length >= 100 && !selected.includes(item.id))}
          onchange={(checked) => choose(item.id, checked)}
        />
        {#if selected.includes(item.id)}
          <Checkbox
            class="publish"
            label="May publish notes"
            ariaLabel="May publish notes to {item.full_name}"
            description="Reports and plans it writes about this repository, marked AI-written and visible to its readers."
            checked={publishing.includes(item.id)}
            disabled={saving}
            onchange={(checked) => allowPublish(item.id, checked)}
          />
        {/if}
      {/each}
    </div>
    {#if total > 50}
      <nav class="pages" aria-label="Source repository pages">
        <Button
          size="sm"
          disabled={offset === 0 || saving}
          onclick={() => (offset = Math.max(0, offset - 50))}>Previous</Button
        >
        <span>{offset + 1}–{Math.min(offset + 50, total)} of {total}</span>
        <Button size="sm" disabled={offset + 50 >= total || saving} onclick={() => (offset += 50)}
          >Next</Button
        >
      </nav>
    {/if}
  {/if}
  {#snippet footer()}
    <Button variant="secondary" disabled={saving} onclick={() => (open = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={saving}
      disabled={loading || !!failure || !initialisedFor}
      onclick={save}>Save source access</Button
    >
  {/snippet}
</Dialog>

<style>
  .guidance,
  .empty {
    color: var(--z-text-subtle);
    margin: 0 0 var(--z-space-4);
  }
  .choices {
    display: grid;
    gap: var(--z-space-4);
    overflow-wrap: anywhere;
  }
  .choices :global(.publish) {
    margin-left: var(--z-space-6);
  }
  .selection-summary,
  .pages {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
  }
  .selection-summary {
    margin-bottom: var(--z-space-4);
  }
  .pages {
    margin-top: var(--z-space-4);
  }
</style>

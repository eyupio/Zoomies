<!--
  The tags on one host: the key/value pairs a pool selects it by.

  A tag is a label stored on the host -- the API and the pool's host selector
  call it that -- so this edits the same map either way. What is shown beside it
  is the controller's own: the tags it works out from the machine, which cannot
  be edited and are not stored, so that an operator can see what a pool's
  selector will match on without guessing at the half that is not theirs.

  Capacity and the reserve are not here. They were, and a host then had two
  ways to set the same two numbers -- this dialog and "Adjust" beside the slot
  bar -- which disagreed about what a good number was, because only one of
  them knew what a runner in this fleet asks for. Adjust owns the resources
  and its recommendations; this owns everything else.
-->
<script lang="ts">
  import { updateHost } from '$lib/api/client';
  import type { Host, SizeClass } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import LabelMapEditor from './LabelMapEditor.svelte';
  import { SIZE_CLASSES, labelsFromRows, overrideNote, tagRows } from './tags';

  interface Props {
    open?: boolean;
    host: Host | null;
    onclose?: () => void;
  }

  let { open = $bindable(false), host, onclose }: Props = $props();

  let rows = $state<{ key: string; value: string }[]>([]);
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
    rows = Object.entries(host.labels ?? {}).map(([key, value]) => ({ key, value }));
  });

  // The tags the controller works out, which are listed and never edited.
  const workedOut = $derived(tagRows(host?.tags).filter((row) => row.automatic));
  const sizeNote = $derived(overrideNote(host?.size_class));

  // Whether the controller is working classes out. While it is, a size tag is a
  // class and nothing else, and the server refuses anything different: said
  // beside the row, because a refusal after the dialog has closed over what was
  // typed is the one place an operator cannot see which row it meant.
  const tracksClasses = $derived(host?.size_class !== undefined);
  function rowError(row: { key: string; value: string }): string | undefined {
    if (!tracksClasses || row.key.trim() !== 'size') return undefined;
    if (SIZE_CLASSES.includes(row.value.trim() as SizeClass)) return undefined;
    return 'While the controller works out size classes, the size tag has to be small, medium or large. Remove it to have the class worked out from the machine.';
  }
  const invalid = $derived(rows.some((row) => rowError(row) !== undefined));

  function close(): void {
    open = false;
    onclose?.();
  }

  async function save(): Promise<void> {
    if (!host?.id || invalid) return;
    saving = true;
    const labels = labelsFromRows(rows, host.labels);
    try {
      await updateHost(host.id, { labels });
      await fleet.reconcile();
      toasts.success(
        `${host.name || host.id} updated`,
        'Pools match against the new tags on the next scheduling pass.',
      );
      close();
    } catch (cause) {
      // Nothing here is a per-field error the form could point at -- the map
      // is one setting -- so the refusal is said once, in the toast.
      toasts.fromError(cause, 'Those tags were not saved');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog
  bind:open
  title="Tags on {host?.name || 'host'}"
  description="Pools choose hosts by these key/value pairs. Capacity and the reserve are under Adjust."
  onclose={close}
>
  <div class="form">
    <Field
      label="Tags"
      hint="A pool with a host selector runs only where every pair it names matches. A tag with no value is a flag, stored as true."
    >
      {#snippet children({ describedBy })}
        <LabelMapEditor
          bind:rows
          {describedBy}
          label="Tags"
          noun="tag"
          empty="No tags of your own. Pools that select hosts by tag will not choose this host."
          {rowError}
        />
      {/snippet}
    </Field>

    {#if workedOut.length > 0 || sizeNote}
      <section class="derived" aria-labelledby="derived-tags-heading" data-testid="derived-tags">
        <h3 id="derived-tags-heading">Worked out by the controller</h3>
        {#if workedOut.length > 0}
          <ul class="tags">
            {#each workedOut as row (row.key)}
              <li class="mono" title={row.hint}>{row.text}</li>
            {/each}
          </ul>
        {/if}
        <p>
          {#if workedOut.length > 0}
            These come from the machine, are not stored and cannot be edited here.
          {/if}
          {#if sizeNote}
            {sizeNote} Remove the size tag to have it worked out from the machine again.
          {:else if tracksClasses}
            To put the host in another class, add a tag called <span class="mono">size</span>
            with the value small, medium or large.
          {/if}
        </p>
      </section>
    {/if}
  </div>

  {#snippet footer()}
    <Button variant="ghost" onclick={close}>Cancel</Button>
    <Button variant="primary" loading={saving} disabled={invalid} onclick={save}>
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
  .derived {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding-top: var(--z-space-3);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  h3 {
    margin: 0;
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
    color: var(--z-text-muted);
    font-weight: var(--z-weight-medium);
  }
  .tags {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .tags li {
    padding: 0 var(--z-space-1);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
  }
  .derived p {
    margin: 0;
    max-width: 60ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
</style>

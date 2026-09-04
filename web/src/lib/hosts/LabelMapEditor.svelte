<!--
  The key/value labels a host carries.

  Host labels are a map, not a list: a pool's `host_selector` matches them by
  key and value, so `arch=arm64` and `arch=amd64` have to stay distinguishable.
  Each row is a pair of fields with its own remove button, which keeps the whole
  thing keyboard operable without inventing a widget.
-->
<script lang="ts">
  import { Plus, Trash2 } from '@lucide/svelte';
  import Button from '$lib/components/Button.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import Input from '$lib/components/Input.svelte';

  interface Props {
    /** The rows being edited. Bound, so the dialog can read them back. */
    rows: { key: string; value: string }[];
    /** Names the group for assistive technology. */
    label?: string;
    describedBy?: string;
    class?: string;
  }

  let {
    rows = $bindable(),
    label = 'Labels',
    describedBy,
    class: className = '',
  }: Props = $props();

  const uid = $props.id();

  const duplicate = $derived.by(() => {
    const seen: string[] = [];
    for (const row of rows) {
      const key = row.key.trim();
      if (!key) continue;
      if (seen.includes(key)) return key;
      seen.push(key);
    }
    return '';
  });

  function add(): void {
    rows = [...rows, { key: '', value: '' }];
  }

  function remove(index: number): void {
    rows = rows.filter((_, i) => i !== index);
  }
</script>

<div class="editor {className}" role="group" aria-label={label} aria-describedby={describedBy}>
  {#if rows.length === 0}
    <p class="empty">
      No labels. Pools select hosts by these, so a host with none matches only pools that ask for
      nothing in particular.
    </p>
  {:else}
    <div class="head" aria-hidden="true">
      <span>Key</span>
      <span>Value</span>
      <span></span>
    </div>
    {#each rows as row, index (index)}
      <div class="row">
        <Input
          bind:value={row.key}
          size="sm"
          mono
          ariaLabel="Label {index + 1} key"
          id="{uid}-key-{index}"
          invalid={Boolean(duplicate) && row.key.trim() === duplicate}
        />
        <Input
          bind:value={row.value}
          size="sm"
          mono
          ariaLabel="Label {index + 1} value"
          id="{uid}-value-{index}"
        />
        <IconButton
          icon={Trash2}
          label="Remove the label {row.key || index + 1}"
          size="sm"
          onclick={() => remove(index)}
        />
      </div>
    {/each}
  {/if}

  {#if duplicate}
    <p class="error">
      Two labels are both called <span class="mono">{duplicate}</span>. The last one would win, so
      rename or remove one.
    </p>
  {/if}

  <div>
    <Button size="sm" variant="secondary" icon={Plus} onclick={add}>Add a label</Button>
  </div>
</div>

<style>
  .editor {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .head,
  .row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) var(--z-space-6);
    align-items: center;
    gap: var(--z-space-2);
  }
  .head span {
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-text-muted);
  }
  .empty {
    margin: 0;
    max-width: 60ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .error {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-danger);
  }
</style>

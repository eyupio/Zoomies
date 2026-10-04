<!--
  The key/value labels a host carries.

  Host labels are a map, not a list: a pool's `host_selector` matches them by
  key and value, so `arch=arm64` and `arch=amd64` have to stay distinguishable.
  Each row is a pair of fields with its own remove button, which keeps the whole
  thing keyboard operable without inventing a widget.

  The same editor serves a join token's labels and a host's tags, which are the
  same map under two names, so what a row is called is a prop.
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
    /** What one row is called: a "label" on a join token, a "tag" on a host. */
    noun?: string;
    /** What to say while there are no rows, where the default would mislead. */
    empty?: string;
    /**
     * Why a row cannot be saved, or undefined where it can. Asked of every row on
     * every change, so a value that is wrong is said to be wrong where it is
     * typed rather than by the server after the dialog has closed over it.
     */
    rowError?: (row: { key: string; value: string }) => string | undefined;
  }

  let {
    rows = $bindable(),
    label = 'Labels',
    describedBy,
    class: className = '',
    noun = 'label',
    empty,
    rowError,
  }: Props = $props();
  const Noun = $derived(noun.charAt(0).toUpperCase() + noun.slice(1));

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
  const errorId = $derived(duplicate ? `${uid}-duplicate-error` : undefined);

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
      {empty ??
        `No ${noun}s. Pools select hosts by these, so a host with none matches only pools that ask for nothing in particular.`}
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
          ariaLabel="{Noun} {index + 1} key"
          id="{uid}-key-{index}"
          invalid={Boolean(duplicate) && row.key.trim() === duplicate}
          describedBy={Boolean(duplicate) && row.key.trim() === duplicate ? errorId : undefined}
        />
        <Input
          bind:value={row.value}
          size="sm"
          mono
          ariaLabel="{Noun} {index + 1} value"
          id="{uid}-value-{index}"
          invalid={rowError?.(row) !== undefined}
          describedBy={rowError?.(row) !== undefined ? `${uid}-row-error-${index}` : undefined}
        />
        <IconButton
          icon={Trash2}
          label="Remove the {noun} {row.key || index + 1}"
          size="sm"
          onclick={() => remove(index)}
        />
      </div>
      {#if rowError?.(row)}
        <!-- role="alert" for the same reason as the duplicate below: the link
             from the input is only read if somebody goes back to it. -->
        <p class="error" id="{uid}-row-error-{index}" role="alert">{rowError(row)}</p>
      {/if}
    {/each}
  {/if}

  {#if duplicate}
    <!-- role="alert" so the failure is spoken when it appears; the
         describedby link on the offending inputs alone is only read if the
         field is revisited. -->
    <p class="error" id={errorId} role="alert">
      Two {noun}s are both called <span class="mono">{duplicate}</span>. The last one would win, so
      rename or remove one.
    </p>
  {/if}

  <div>
    <Button size="sm" variant="secondary" icon={Plus} onclick={add}>Add a {noun}</Button>
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
    letter-spacing: var(--z-tracking-wide);
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

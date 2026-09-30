<!--
  Importing pools.

  A pools export from another instance, or the copy kept in a repository,
  read back pool by pool. As with the settings import, the preview is the
  point: before anything is written the dialog says which pools would be
  created, which would change and in which settings, which are already so, and
  which this instance refuses and why -- and lets the operator leave pools out.
  Applying is one change or none.
-->
<script lang="ts">
  import { FileUp, TriangleAlert } from '@lucide/svelte';
  import { SvelteSet } from 'svelte/reactivity';
  import { ApiError, importPools } from '$lib/api/client';
  import type { PoolsImport, PoolsImportChange } from '$lib/api/types';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  import { tableLayout } from '$lib/actions/tableLayout';

  interface Props {
    open?: boolean;
    /** Called after an import is applied, so the page can refresh. */
    onapplied?: () => void;
  }

  let { open = $bindable(false), onapplied }: Props = $props();

  let document = $state('');
  let filename = $state('');
  let preview = $state<PoolsImport | null>(null);
  let busy = $state(false);
  let error = $state('');
  const skipped = new SvelteSet<string>();
  let fileInput = $state<HTMLInputElement | null>(null);

  $effect(() => {
    if (!open) {
      document = '';
      filename = '';
      preview = null;
      error = '';
      skipped.clear();
    }
  });

  async function pickFile(event: Event): Promise<void> {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;
    filename = file.name;
    document = await file.text();
    preview = null;
    error = '';
  }

  async function look(): Promise<void> {
    busy = true;
    error = '';
    try {
      preview = await importPools({ document, dry_run: true });
      // Refused pools start out skipped, so the apply button is honest about
      // what it would do; the operator can tick one back once it is fixed.
      skipped.clear();
      for (const c of preview.changes) {
        if (c.action === 'refused') skipped.add(c.pool);
      }
    } catch (cause) {
      if (cause instanceof ApiError) {
        const fields = cause.fieldErrors();
        error = fields.document ?? cause.message;
      } else {
        error = 'The document could not be read.';
      }
    } finally {
      busy = false;
    }
  }

  async function apply(): Promise<void> {
    busy = true;
    error = '';
    try {
      const result = await importPools({ document, skip: [...skipped] });
      onapplied?.();
      open = false;
      const n = result.summary.create + result.summary.change;
      toasts.success(
        n === 0 ? 'Nothing to change' : `${n} ${n === 1 ? 'pool' : 'pools'} imported`,
        n === 0
          ? 'Every pool in the document was already so.'
          : 'Environment values are never in an export; set any the document names on each pool.',
      );
    } catch (cause) {
      toasts.fromError(cause, 'The import was not applied');
      if (cause instanceof ApiError && cause.errors?.length) {
        // Re-plan so the rows show the refusals the apply found.
        await look();
      }
    } finally {
      busy = false;
    }
  }

  function toggle(pool: string, on: boolean): void {
    if (on) skipped.delete(pool);
    else skipped.add(pool);
  }

  const willWrite = $derived(
    (preview?.changes ?? []).filter(
      (c) => (c.action === 'create' || c.action === 'change') && !skipped.has(c.pool),
    ).length,
  );
  const stillRefused = $derived(
    (preview?.changes ?? []).filter((c) => c.action === 'refused' && !skipped.has(c.pool)).length,
  );
  const envKeys = $derived(
    (preview?.changes ?? []).filter(
      (c) => c.action !== 'refused' && c.env_keys.length > 0 && !skipped.has(c.pool),
    ),
  );

  function render(value: unknown): string {
    if (value === null || value === undefined) return 'not set';
    if (typeof value === 'boolean') return value ? 'on' : 'off';
    if (Array.isArray(value)) return value.length ? value.join(', ') : 'none';
    if (typeof value === 'object') return JSON.stringify(value);
    return String(value) === '' ? 'not set' : String(value);
  }

  function tone(action: PoolsImportChange['action']): 'idle' | 'neutral' | 'pending' | 'danger' {
    switch (action) {
      case 'create':
        return 'idle';
      case 'change':
        return 'pending';
      case 'refused':
        return 'danger';
      default:
        return 'neutral';
    }
  }
  function label(action: PoolsImportChange['action']): string {
    switch (action) {
      case 'create':
        return 'Will create';
      case 'change':
        return 'Will change';
      case 'refused':
        return 'Refused';
      default:
        return 'Already so';
    }
  }
</script>

<Dialog
  bind:open
  title="Import pools"
  description="A pools export from another instance, or the copy you keep in a repository. Every pool is checked and shown before anything is written."
  size="lg"
>
  <div class="body">
    {#if !preview}
      <div class="pick">
        <!-- The button beside it is the control. A tab stop here would be clipped away with the input and draw no focus ring. -->
        <input
          bind:this={fileInput}
          type="file"
          accept=".json,.yaml,.yml,application/json,application/yaml"
          tabindex="-1"
          class="sr-only"
          aria-label="Choose a pools export"
          onchange={pickFile}
        />
        <Button variant="secondary" icon={FileUp} onclick={() => fileInput?.click()}
          >Choose a file</Button
        >
        <span class="filename" class:unset={!filename}
          >{filename || 'or paste the document below'}</span
        >
      </div>
      <Field
        label="Document"
        hint="JSON or YAML. Pools are matched by name; a pool the document does not name is left alone."
        {error}
      >
        {#snippet children({ id, describedBy, invalid })}
          <Textarea
            bind:value={document}
            {id}
            {describedBy}
            {invalid}
            rows={10}
            mono
            placeholder="pools:&#10;  - name: zoomies-linux-x64&#10;    installation: acme&#10;    max_runners: 8"
          />
        {/snippet}
      </Field>
    {:else}
      <p class="summary">
        <strong>{willWrite}</strong>
        {willWrite === 1 ? 'pool' : 'pools'} will be created or changed,
        <strong>{preview.summary.unchanged}</strong> already so,
        {#if stillRefused > 0}
          <strong class="refused-count">{stillRefused}</strong> refused —
          {stillRefused === 1 ? 'it is' : 'they are'} left out until the document is fixed.
        {:else if preview.summary.refused > 0}
          {preview.summary.refused} refused and left out.
        {/if}
      </p>
      {#if envKeys.length > 0}
        <p class="secrets">
          <TriangleAlert size={14} aria-hidden="true" />
          <span>
            An export never carries environment values. Set these by hand on each pool afterwards:
            {#each envKeys as c, i (c.pool)}
              <span class="mono">{c.pool}: {c.env_keys.join(', ')}</span>{i < envKeys.length - 1
                ? '; '
                : '.'}
            {/each}
          </span>
        </p>
      {/if}
      <div class="scroll">
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <table
          role="table"
          use:tableLayout={{
            id: 'pools-import',
            columns: ['include', 'pool', 'settings', 'outcome'],
          }}
        >
          <caption class="sr-only">What the import would do to each pool</caption>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <thead role="rowgroup">
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <tr role="row">
              <th role="columnheader" scope="col"><span class="sr-only">Include</span></th>
              <th role="columnheader" scope="col">Pool</th>
              <th role="columnheader" scope="col">Settings</th>
              <th role="columnheader" scope="col">Outcome</th>
            </tr>
          </thead>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tbody role="rowgroup">
            {#each preview.changes as change, i (`${change.pool}-${i}`)}
              <tr role="row" class:skipped={skipped.has(change.pool)}>
                <td role="cell" data-label="Include" class="tick">
                  {#if change.action !== 'unchanged'}
                    <Checkbox
                      checked={!skipped.has(change.pool)}
                      ariaLabel="Include {change.pool}"
                      onchange={(on) => toggle(change.pool, on)}
                    />
                  {/if}
                </td>
                <td role="cell" data-label="Pool">
                  <span class="key mono">{change.pool}</span>
                </td>
                <td role="cell" data-label="Settings" class="value">
                  {#if change.action === 'create'}
                    <span class="second">A new pool, with every setting the document gives it.</span
                    >
                  {:else if change.fields.length > 0}
                    <ul class="fields">
                      {#each change.fields as f (f.field)}
                        <li>
                          <span class="mono">{f.field}</span>
                          <span class="mono now">{render(f.current)}</span>
                          <span aria-hidden="true">→</span>
                          <span class="sr-only">becomes</span>
                          <span class="mono">{render(f.incoming)}</span>
                        </li>
                      {/each}
                    </ul>
                  {:else if change.action === 'unchanged'}
                    <span class="second">Every setting is already so.</span>
                  {/if}
                </td>
                <td role="cell" data-label="Outcome">
                  <Badge
                    tone={tone(change.action)}
                    label={label(change.action)}
                    size="sm"
                    dot={false}
                  />
                  {#if change.reason}<span class="second reason">{change.reason}</span>{/if}
                  {#each change.warnings as w (w)}
                    <span class="second warning">{w}</span>
                  {/each}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>
  {#snippet footer()}
    {#if !preview}
      <Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
      <Button variant="primary" onclick={look} loading={busy} disabled={!document.trim()}
        >Check the document</Button
      >
    {:else}
      <Button variant="ghost" onclick={() => (preview = null)} disabled={busy}>Back</Button>
      <Button
        variant="primary"
        onclick={apply}
        loading={busy}
        disabled={willWrite === 0 && stillRefused === 0}
      >
        {willWrite === 0
          ? 'Nothing to apply'
          : `Apply to ${willWrite} ${willWrite === 1 ? 'pool' : 'pools'}`}
      </Button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    padding-bottom: var(--z-space-2);
  }
  .pick {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-3);
  }
  .filename {
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .unset {
    color: var(--z-text-subtle);
    font-style: italic;
  }
  .summary {
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  .summary strong {
    color: var(--z-text);
  }
  .refused-count {
    color: var(--z-danger);
  }
  .secrets {
    display: flex;
    gap: var(--z-space-2);
    align-items: flex-start;
    margin: 0;
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-pending-subtle);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text);
  }
  .secrets :global(svg) {
    flex: none;
    color: var(--z-pending);
  }
  .scroll {
    overflow-x: auto;
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
  }
  table {
    width: 100%;
    table-layout: fixed;
    border-collapse: separate;
    border-spacing: 0;
    font-size: var(--z-text-sm);
  }
  th {
    padding: var(--z-space-2) var(--z-space-3);
    border-bottom: var(--z-border-width) solid var(--z-border);
    background: var(--z-surface-sunken);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-align: left;
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
  }
  td {
    overflow-wrap: anywhere;
    padding: var(--z-space-2) var(--z-space-3);
    border-bottom: var(--z-border-width) solid var(--z-border);
    vertical-align: top;
  }
  tbody tr:last-child td {
    border-bottom: 0;
  }
  tr.skipped td {
    color: var(--z-text-subtle);
  }
  th:first-child,
  .tick {
    width: 2.5rem;
  }
  .key {
    font-size: var(--z-text-xs);
    color: var(--z-text);
  }
  .value {
    font-size: var(--z-text-xs);
  }
  .fields {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .now {
    color: var(--z-text-subtle);
  }
  .second {
    display: block;
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-subtle);
  }
  .reason {
    color: var(--z-danger);
  }
  .warning {
    color: var(--z-pending);
  }
  @media (max-width: 768px) {
    thead {
      display: none;
    }
    table,
    tbody,
    tr,
    td {
      display: block;
      width: auto;
    }
    tr {
      padding: var(--z-space-2) var(--z-space-3);
      border-bottom: var(--z-border-width) solid var(--z-border);
    }
    td {
      padding: var(--z-space-1) 0;
      border-bottom: 0;
    }
    td::before {
      content: attr(data-label);
      display: block;
      font-size: var(--z-text-2xs);
      text-transform: uppercase;
      letter-spacing: var(--z-tracking-wide);
      color: var(--z-text-muted);
    }
    .tick::before {
      content: none;
    }
  }
</style>

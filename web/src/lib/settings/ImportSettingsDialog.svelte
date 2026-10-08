<!--
  Importing a configuration.

  An export from another controller, or a zoomies.yaml somebody kept, read
  back key by key. The whole point is the preview: before anything is written,
  the dialog says for each key whether it would change, is already so, or is
  refused and why -- and lets the operator leave keys out. Applying is one
  change or none, exactly as a PATCH is.
-->
<script lang="ts">
  import { FileUp, TriangleAlert } from '@lucide/svelte';
  import { SvelteSet } from 'svelte/reactivity';
  import { ApiError, importSettings } from '$lib/api/client';
  import type { Settings, SettingsImport, SettingsImportChange } from '$lib/api/types';
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
    /** Called with the settings page after an import is applied. */
    onapplied: (settings: Settings) => void;
  }

  let { open = $bindable(false), onapplied }: Props = $props();

  let document = $state('');
  let filename = $state('');
  let preview = $state<SettingsImport | null>(null);
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
      preview = await importSettings({ document, dry_run: true });
      // Refused keys start out skipped: the operator can put them back once
      // the document is fixed, and until then the apply button is honest.
      skipped.clear();
      for (const c of preview.changes) {
        if (c.action === 'refused' && c.key) skipped.add(c.key);
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
      const result = await importSettings({ document, skip: [...skipped] });
      if (result.settings) onapplied(result.settings);
      open = false;
      const n = result.summary.change + result.summary.unset;
      toasts.success(
        n === 0 ? 'Nothing to change' : `${n} ${n === 1 ? 'setting' : 'settings'} imported`,
        n === 0
          ? 'Every key in the document was already so.'
          : 'They are stored in this fleet. Those that wait for a restart are marked on the page.',
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

  function toggle(key: string, on: boolean): void {
    if (on) skipped.delete(key);
    else skipped.add(key);
  }

  const willChange = $derived(
    (preview?.changes ?? []).filter(
      (c) => (c.action === 'change' || c.action === 'unset') && !skipped.has(c.key),
    ).length,
  );
  const stillRefused = $derived(
    (preview?.changes ?? []).filter(
      (c) => c.action === 'refused' && (!c.key || !skipped.has(c.key)),
    ).length,
  );

  function render(value: unknown): string {
    if (value === null || value === undefined) return 'not set';
    if (typeof value === 'boolean') return value ? 'on' : 'off';
    if (Array.isArray(value)) return value.length ? value.join(', ') : 'none';
    if (typeof value === 'object') return JSON.stringify(value);
    return String(value) === '' ? 'not set' : String(value);
  }

  function tone(action: SettingsImportChange['action']): 'idle' | 'neutral' | 'pending' | 'danger' {
    switch (action) {
      case 'change':
        return 'idle';
      case 'unset':
        return 'pending';
      case 'refused':
        return 'danger';
      default:
        return 'neutral';
    }
  }
  function label(action: SettingsImportChange['action']): string {
    switch (action) {
      case 'change':
        return 'Will change';
      case 'unset':
        return 'Will reset';
      case 'refused':
        return 'Refused';
      default:
        return 'Already so';
    }
  }
</script>

<Dialog
  bind:open
  title="Import settings"
  description="An export from a controller, or a zoomies.yaml. Every key is checked and shown before anything is written."
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
          aria-label="Choose an export file"
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
        hint="JSON or YAML. Secrets are never in an export; set those by hand afterwards."
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
            placeholder="retention:&#10;  jobs: 720h"
          />
        {/snippet}
      </Field>
    {:else}
      <p class="summary">
        <strong>{willChange}</strong>
        {willChange === 1 ? 'setting' : 'settings'} will change,
        <strong>{preview.summary.unchanged}</strong> already so,
        {#if stillRefused > 0}
          <strong class="refused-count">{stillRefused}</strong> refused,
          {stillRefused === 1 ? 'it is' : 'they are'} left out until the document is fixed.
        {:else if preview.summary.refused > 0}
          {preview.summary.refused} refused and left out.
        {/if}
      </p>
      {#if (preview.secrets_configured ?? []).length > 0}
        <p class="secrets">
          <TriangleAlert size={14} aria-hidden="true" />
          <span>
            The source had {preview.secrets_configured.length === 1 ? 'a secret' : 'secrets'} set that
            an export cannot carry:
            <span class="mono">{preview.secrets_configured.join(', ')}</span>. Set
            {preview.secrets_configured.length === 1 ? 'it' : 'them'} on the Configuration tab afterwards.
          </span>
        </p>
      {/if}
      <div class="scroll">
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <table
          role="table"
          use:tableLayout={{
            id: 'settings-import',
            columns: ['include', 'setting', 'current', 'incoming', 'outcome'],
          }}
        >
          <caption class="sr-only">What the import would change</caption>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <thead role="rowgroup">
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <tr role="row">
              <th role="columnheader" scope="col"><span class="sr-only">Include</span></th>
              <th role="columnheader" scope="col">Setting</th>
              <th role="columnheader" scope="col">Now</th>
              <th role="columnheader" scope="col">Incoming</th>
              <th role="columnheader" scope="col">Outcome</th>
            </tr>
          </thead>
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tbody role="rowgroup">
            {#each preview.changes as change, i (change.key || `general-${i}`)}
              <tr role="row" class:skipped={change.key ? skipped.has(change.key) : false}>
                <td role="cell" data-label="Include" class="tick">
                  {#if change.key && change.action !== 'unchanged'}
                    <Checkbox
                      checked={!skipped.has(change.key)}
                      ariaLabel="Include {change.key}"
                      onchange={(on) => toggle(change.key, on)}
                    />
                  {/if}
                </td>
                <td role="cell" data-label="Setting">
                  {#if change.key}
                    <span class="key mono">{change.key}</span>
                    {#if change.label}<span class="second">{change.label}</span>{/if}
                  {:else}
                    <span class="second">The document as a whole</span>
                  {/if}
                </td>
                <td role="cell" data-label="Now" class="value mono">
                  {change.secret ? 'set, not shown' : render(change.current)}
                </td>
                <td role="cell" data-label="Incoming" class="value mono">
                  {change.secret ? 'set, not shown' : render(change.incoming)}
                </td>
                <td role="cell" data-label="Outcome">
                  <Badge
                    tone={tone(change.action)}
                    label={label(change.action)}
                    size="sm"
                    dot={false}
                  />
                  {#if change.action === 'change' && !change.live}
                    <span class="second">after a restart</span>
                  {/if}
                  {#if change.reason}<span class="second reason">{change.reason}</span>{/if}
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
        disabled={willChange === 0 && stillRefused === 0}
      >
        {willChange === 0
          ? 'Nothing to apply'
          : `Apply ${willChange} ${willChange === 1 ? 'change' : 'changes'}`}
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
  .second {
    display: block;
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-subtle);
  }
  .reason {
    color: var(--z-danger);
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

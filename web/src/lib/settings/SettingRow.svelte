<!--
  One configuration setting.

  Three states in one row: read-only (most of them), editable now, and
  changeable only by editing the file and restarting. The third is said plainly
  rather than being discovered by trying: the API publishes which keys those
  are, so the row can be honest before anybody clicks.

  Any finding the validator produced about this setting is shown right here,
  because a warning three screens away from the thing it is about is a warning
  nobody acts on.
-->
<script lang="ts">
  import { Check, Pencil, X } from '@lucide/svelte';
  import type { Problem } from '$lib/api/types';
  import { severityStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';

  interface Props {
    /** The dotted key, as it is written in the configuration file. */
    path: string;
    value: unknown;
    /** Can be changed on the running controller. */
    editable?: boolean;
    /** Exists, but only the file and a restart can change it. */
    restartRequired?: boolean;
    findings?: readonly Problem[];
    /** Options when the value is one of a fixed set, such as the log level. */
    choices?: readonly { value: string; label: string }[];
    /** Returns an error message, or an empty string when the change was accepted. */
    onsave: (path: string, value: string) => Promise<string>;
    class?: string;
  }

  let {
    path,
    value,
    editable = false,
    restartRequired = false,
    findings = [],
    choices,
    onsave,
    class: className = '',
  }: Props = $props();

  const leaf = $derived(path.split('.').slice(1).join('.') || path);
  const numeric = $derived(typeof value === 'number');

  function display(raw: unknown): string {
    if (raw === null || raw === undefined) return 'not set';
    if (typeof raw === 'boolean') return raw ? 'on' : 'off';
    if (Array.isArray(raw)) return raw.length > 0 ? raw.join(', ') : 'none';
    if (typeof raw === 'object') {
      const entries = Object.entries(raw as Record<string, unknown>);
      return entries.length > 0 ? entries.map(([k, v]) => `${k}=${String(v)}`).join(' ') : 'none';
    }
    return String(raw) === '' ? 'not set' : String(raw);
  }

  const shown = $derived(display(value));
  const unset = $derived(shown === 'not set' || shown === 'none');

  let editing = $state(false);
  let draft = $state('');
  let saving = $state(false);
  let failure = $state('');

  function start(): void {
    draft = value === null || value === undefined ? '' : String(value);
    failure = '';
    editing = true;
  }

  function cancel(): void {
    editing = false;
    failure = '';
  }

  async function commit(): Promise<void> {
    saving = true;
    const message = await onsave(path, draft.trim());
    saving = false;
    if (message) {
      failure = message;
      return;
    }
    editing = false;
  }
</script>

<div class="row {className}" class:has-findings={findings.length > 0}>
  <div class="key">
    <span class="leaf mono">{leaf}</span>
    {#if restartRequired}
      <Badge
        tone="draining"
        label="Needs a restart"
        size="sm"
        dot={false}
        title="This can only be changed in the configuration file, and takes effect when the controller restarts."
      />
    {/if}
  </div>

  <div class="value">
    {#if editing}
      <div class="editor">
        {#if choices}
          <Select bind:value={draft} options={choices} size="sm" ariaLabel="New value for {path}" />
        {:else}
          <Input
            bind:value={draft}
            size="sm"
            mono
            type={numeric ? 'number' : 'text'}
            ariaLabel="New value for {path}"
          />
        {/if}
        <IconButton
          icon={Check}
          label="Save {path}"
          size="sm"
          variant="secondary"
          loading={saving}
          onclick={commit}
        />
        <IconButton icon={X} label="Cancel editing {path}" size="sm" onclick={cancel} />
      </div>
      {#if failure}
        <p class="failure" role="alert">{failure}</p>
      {/if}
    {:else}
      <span class="shown mono" class:unset>{shown}</span>
      {#if editable}
        <Button size="sm" variant="ghost" icon={Pencil} onclick={start}>Change</Button>
      {/if}
    {/if}
  </div>

  {#if findings.length > 0}
    <ul class="findings">
      {#each findings as finding (finding.code)}
        {@const meta = severityStatus(finding.severity)}
        <li>
          <Badge status={meta} size="sm" />
          <div>
            <p class="finding-title">{finding.title}</p>
            {#if finding.detail}<p class="finding-detail">{finding.detail}</p>{/if}
            {#if finding.fix}<p class="finding-fix"><strong>Fix:</strong> {finding.fix}</p>{/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .row {
    display: grid;
    grid-template-columns: minmax(0, 15rem) minmax(0, 1fr);
    gap: var(--z-space-2) var(--z-space-4);
    padding: var(--z-space-3) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
  }
  .row.has-findings {
    background: var(--z-surface-sunken);
  }
  .key {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .leaf {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .value {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .shown {
    font-size: var(--z-text-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .shown.unset {
    color: var(--z-text-subtle);
    font-style: italic;
  }
  .editor {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .failure {
    flex-basis: 100%;
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-danger);
  }
  .findings {
    grid-column: 1 / -1;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .findings li {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
  }
  .finding-title {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .finding-detail,
  .finding-fix {
    margin: var(--z-space-1) 0 0;
    max-width: 76ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
</style>

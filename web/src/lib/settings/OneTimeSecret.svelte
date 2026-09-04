<!--
  A credential the server shows exactly once.

  Join tokens and API tokens are both minted the same way: the plaintext exists
  in this response and nowhere else, because only its hash was stored. The
  component says so plainly, gives one obvious way to take a copy, and does not
  pretend the value can be retrieved later.
-->
<script lang="ts">
  import { KeyRound } from '@lucide/svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';

  interface Props {
    /** The thing being shown: "join token", "API token". */
    what: string;
    /** The secret itself, or the whole command that carries it. */
    value: string;
    /** A label for the copy button: "Copy the install command". */
    copyLabel?: string;
    /** One more line: when it expires, what to do with it next. */
    note?: string;
    class?: string;
  }

  let { what, value, copyLabel, note, class: className = '' }: Props = $props();
</script>

<div class="secret {className}">
  <p class="heading">
    <KeyRound size={14} aria-hidden="true" />
    <span>This {what} is shown once</span>
  </p>
  <p class="explain">
    Zoomies stored only its hash, so it cannot be shown again. Copy it now; if it is lost, revoke it
    and mint another.
  </p>
  <pre class="value mono">{value}</pre>
  <div class="actions">
    <CopyButton {value} label={copyLabel ?? `Copy the ${what}`} size="md" />
    {#if note}<span class="note">{note}</span>{/if}
  </div>
</div>

<style>
  .secret {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding: var(--z-space-4);
    border: 1px solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
  }
  .heading {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .explain {
    margin: 0;
    max-width: 68ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .value {
    margin: 0;
    padding: var(--z-space-3);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface);
    color: var(--z-text);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .actions {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-3);
  }
  .note {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
</style>

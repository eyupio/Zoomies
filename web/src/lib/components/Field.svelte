<!--
  Label, control, hint and error, wired together.

  This is the only way a form control is laid out. The snippet receives the ids
  it must use, so the error is always announced through `aria-describedby` and
  the label always points at the right thing.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';

  interface FieldContext {
    id: string;
    describedBy: string | undefined;
    invalid: boolean;
  }

  interface Props {
    label: string;
    /** Supply one when something outside needs to reference the control. */
    id?: string;
    hint?: string;
    error?: string;
    required?: boolean;
    /** Renders the label visually hidden, for a control whose purpose is obvious. */
    hideLabel?: boolean;
    class?: string;
    children: Snippet<[FieldContext]>;
  }

  let {
    label,
    id: providedId,
    hint,
    error,
    required = false,
    hideLabel = false,
    class: className = '',
    children,
  }: Props = $props();

  // `$props.id()` is stable for the life of the instance, which a random
  // fallback in a prop default would not be.
  const uid = $props.id();
  const id = $derived(providedId ?? `field-${uid}`);
  const hintId = $derived(hint ? `${id}-hint` : undefined);
  const errorId = $derived(error ? `${id}-error` : undefined);
  const describedBy = $derived([errorId, hintId].filter(Boolean).join(' ') || undefined);
</script>

<div class="field {className}">
  <label for={id} class:sr-only={hideLabel}>
    {label}
    {#if required}<span class="required" aria-hidden="true">*</span><span class="sr-only"
        >(required)</span
      >{/if}
  </label>
  {@render children({ id, describedBy, invalid: Boolean(error) })}
  {#if error}
    <p class="error" id={errorId}>{error}</p>
  {:else if hint}
    <p class="hint" id={hintId}>{hint}</p>
  {/if}
</div>

<style>
  .field {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  label {
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .required {
    color: var(--z-danger);
    margin-inline-start: 2px;
  }
  .hint,
  .error {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .hint {
    color: var(--z-text-subtle);
  }
  .error {
    color: var(--z-danger);
  }
</style>

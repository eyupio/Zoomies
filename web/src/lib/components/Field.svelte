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
    /**
     * Something true about the control right now that is neither its
     * permanent explanation nor a validation failure -- caps lock being on,
     * most usefully.
     *
     * It has a slot of its own because error and hint are alternatives: a
     * password that is too short shows its counter, and the caps-lock warning
     * passed through `hint` used to vanish at exactly the moment caps lock was
     * the reason the password was wrong. It is polite-live, so it is heard
     * when it appears rather than only if the field is re-read.
     */
    notice?: string;
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
    notice,
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
  const noticeId = $derived(notice ? `${id}-notice` : undefined);
  const errorId = $derived(error ? `${id}-error` : undefined);
  const describedBy = $derived([errorId, noticeId, hintId].filter(Boolean).join(' ') || undefined);
</script>

<div class="field {className}">
  <label for={id} class:sr-only={hideLabel}>
    {label}
    {#if required}<span class="required" aria-hidden="true">*</span><span class="sr-only"
        >(required)</span
      >{/if}
  </label>
  {@render children({ id, describedBy, invalid: Boolean(error) })}
  {#if notice}
    <p class="notice" id={noticeId} aria-live="polite">{notice}</p>
  {/if}
  <!--
    Error and hint are rendered together, not as alternatives.

    Two reasons. The describedby list names both, so dropping one left the
    control pointing at an element that does not exist. And the hints here are
    instructions, not decoration: the GitHub target field's hint explains that a
    repository App is created on your own account, and it used to vanish the
    moment the operator got the format wrong -- telling them what was wrong just
    as it stopped telling them why.
  -->
  {#if error}
    <!-- role="alert" so a validation failure is spoken when it appears; the
         describedby link alone is only read if the field is revisited. -->
    <p class="error" id={errorId} role="alert">{error}</p>
  {/if}
  {#if hint}
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
  .notice,
  .error {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .hint {
    color: var(--z-text-subtle);
  }
  /*
    Muted, not a status colour. The status palette is a fixed mapping operators
    learn -- amber means provisioning -- and spending it on "caps lock is on"
    teaches that association wrongly on the first screen of the product, before
    they have ever seen a runner. Position and the live region do the work.
  */
  .notice {
    color: var(--z-text-muted);
    font-weight: var(--z-weight-medium);
  }
  .error {
    color: var(--z-danger);
  }
</style>

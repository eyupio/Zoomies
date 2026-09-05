<script lang="ts">
  interface Props {
    checked?: boolean;
    /** Mixed state, for a "select all" that only covers some rows. */
    indeterminate?: boolean;
    label?: string;
    description?: string;
    id?: string;
    name?: string;
    value?: string;
    disabled?: boolean;
    describedBy?: string;
    ariaLabel?: string;
    onchange?: (checked: boolean) => void;
    class?: string;
  }

  let {
    checked = $bindable(false),
    indeterminate = false,
    label,
    description,
    id: providedId,
    name,
    value,
    disabled = false,
    describedBy,
    ariaLabel,
    onchange,
    class: className = '',
  }: Props = $props();

  const uid = $props.id();
  const id = $derived(providedId ?? `checkbox-${uid}`);
  const descriptionId = $derived(description ? `${id}-description` : undefined);
  let element = $state<HTMLInputElement | null>(null);

  $effect(() => {
    if (element) element.indeterminate = indeterminate;
  });
</script>

<div class="row {className}">
  <input
    bind:this={element}
    bind:checked
    {id}
    {name}
    {value}
    {disabled}
    type="checkbox"
    aria-label={ariaLabel ?? (label ? undefined : 'Select')}
    aria-describedby={[describedBy, descriptionId].filter(Boolean).join(' ') || undefined}
    onchange={() => onchange?.(checked)}
  />
  {#if label}
    <div class="text">
      <label for={id}>{label}</label>
      {#if description}<p id={descriptionId}>{description}</p>{/if}
    </div>
  {/if}
</div>

<style>
  .row {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
  }
  input {
    flex: none;
    width: 15px;
    height: 15px;
    margin: 3px 0 0;
    accent-color: var(--z-accent);
    cursor: pointer;
  }
  input:disabled {
    cursor: not-allowed;
    opacity: 0.5;
  }
  .text {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  label {
    font-size: var(--z-text-base);
    color: var(--z-text);
    cursor: pointer;
  }
  /* A label that still invites a click is how a disabled row reads as broken
     rather than as a state, so it stops inviting one. */
  .row:has(input:disabled) label {
    color: var(--z-text-muted);
    cursor: not-allowed;
  }
  p {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
</style>

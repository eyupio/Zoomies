<!--
  A switch is for a setting that takes effect immediately. A checkbox is for a
  value that is submitted with a form. They are not interchangeable.
-->
<script lang="ts">
  interface Props {
    checked?: boolean;
    label: string;
    description?: string;
    id?: string;
    disabled?: boolean;
    describedBy?: string;
    /** Hide the visible label; the accessible name still comes from `label`. */
    hideLabel?: boolean;
    onchange?: (checked: boolean) => void;
    class?: string;
  }

  let {
    checked = $bindable(false),
    label,
    description,
    id: providedId,
    disabled = false,
    describedBy,
    hideLabel = false,
    onchange,
    class: className = '',
  }: Props = $props();

  const uid = $props.id();
  const id = $derived(providedId ?? `switch-${uid}`);
  const descriptionId = $derived(description ? `${id}-description` : undefined);

  function toggle(): void {
    if (disabled) return;
    checked = !checked;
    onchange?.(checked);
  }
</script>

<div class="row {className}">
  <button
    {id}
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={hideLabel ? label : undefined}
    aria-describedby={[describedBy, descriptionId].filter(Boolean).join(' ') || undefined}
    {disabled}
    class="track"
    onclick={toggle}
  >
    <span class="thumb"></span>
  </button>
  {#if !hideLabel}
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
    gap: var(--z-space-3);
  }
  .track {
    flex: none;
    position: relative;
    width: var(--z-space-8);
    height: var(--z-space-5);
    margin-top: 1px;
    padding: 0;
    border: 1px solid var(--z-border-strong);
    border-radius: var(--z-radius-full);
    background: var(--z-surface-sunken);
    cursor: pointer;
    transition:
      background-color var(--z-motion-fast) var(--z-ease),
      border-color var(--z-motion-fast) var(--z-ease);
  }
  .track[aria-checked='true'] {
    background: var(--z-accent);
    border-color: var(--z-accent);
  }
  .track:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .thumb {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 14px;
    height: 14px;
    border-radius: var(--z-radius-full);
    background: var(--z-surface);
    box-shadow: var(--z-shadow-sm);
    transition: transform var(--z-motion-fast) var(--z-ease);
  }
  .track[aria-checked='true'] .thumb {
    transform: translateX(var(--z-space-3));
    background: var(--z-accent-contrast);
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
  p {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
</style>

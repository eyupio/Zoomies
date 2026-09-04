<!--
  A radio group with real radio inputs, so arrow-key navigation and the
  "one tab stop for the whole group" behaviour come from the browser rather
  than from us reimplementing them slightly wrong.
-->
<script lang="ts">
  export interface RadioOption {
    value: string;
    label: string;
    description?: string;
    disabled?: boolean;
  }

  interface Props {
    value?: string;
    options: readonly RadioOption[];
    name: string;
    legend?: string;
    /** Lay the options out in a row rather than a column. */
    inline?: boolean;
    disabled?: boolean;
    describedBy?: string;
    onchange?: (value: string) => void;
    class?: string;
  }

  let {
    value = $bindable(''),
    options,
    name,
    legend,
    inline = false,
    disabled = false,
    describedBy,
    onchange,
    class: className = '',
  }: Props = $props();
</script>

<fieldset class={className} aria-describedby={describedBy}>
  {#if legend}<legend>{legend}</legend>{/if}
  <div class="options" class:inline>
    {#each options as option (option.value)}
      <label class="option" class:disabled={disabled || option.disabled}>
        <input
          type="radio"
          {name}
          value={option.value}
          checked={value === option.value}
          disabled={disabled || option.disabled}
          onchange={() => {
            value = option.value;
            onchange?.(option.value);
          }}
        />
        <span class="text">
          <span class="label">{option.label}</span>
          {#if option.description}<span class="description">{option.description}</span>{/if}
        </span>
      </label>
    {/each}
  </div>
</fieldset>

<style>
  fieldset {
    margin: 0;
    padding: 0;
    border: 0;
  }
  legend {
    padding: 0 0 var(--z-space-2);
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .options {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .options.inline {
    flex-direction: row;
    flex-wrap: wrap;
    gap: var(--z-space-4);
  }
  .option {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    cursor: pointer;
  }
  .option.disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }
  input {
    flex: none;
    width: 15px;
    height: 15px;
    margin: 3px 0 0;
    accent-color: var(--z-accent);
  }
  .text {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .label {
    font-size: var(--z-text-base);
    color: var(--z-text);
  }
  .description {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
</style>

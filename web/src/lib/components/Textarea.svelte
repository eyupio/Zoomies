<script lang="ts">
  interface Props {
    value?: string;
    id?: string;
    name?: string;
    placeholder?: string;
    rows?: number;
    disabled?: boolean;
    readonly?: boolean;
    required?: boolean;
    invalid?: boolean;
    describedBy?: string;
    ariaLabel?: string;
    mono?: boolean;
    element?: HTMLTextAreaElement | null;
    oninput?: (event: Event) => void;
    onblur?: (event: FocusEvent) => void;
    class?: string;
  }

  let {
    value = $bindable(''),
    id,
    name,
    placeholder,
    rows = 4,
    disabled = false,
    readonly = false,
    required = false,
    invalid = false,
    describedBy,
    ariaLabel,
    mono = false,
    element = $bindable(null),
    oninput,
    onblur,
    class: className = '',
  }: Props = $props();
</script>

<textarea
  bind:this={element}
  bind:value
  {id}
  {name}
  {placeholder}
  {rows}
  {disabled}
  {readonly}
  {required}
  class={className}
  class:mono
  aria-label={ariaLabel}
  aria-invalid={invalid ? 'true' : undefined}
  aria-describedby={describedBy}
  {oninput}
  {onblur}></textarea>

<style>
  textarea {
    width: 100%;
    padding: var(--z-space-2) var(--z-space-3);
    font-family: var(--z-font-sans);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
    background: var(--z-surface);
    border: 1px solid var(--z-border-strong);
    border-radius: var(--z-radius-sm);
    resize: vertical;
  }
  textarea.mono {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-sm);
  }
  textarea::placeholder {
    color: var(--z-text-subtle);
  }
  textarea:disabled {
    background: var(--z-surface-sunken);
    cursor: not-allowed;
  }
  textarea[aria-invalid='true'] {
    border-color: var(--z-danger);
  }

  /*
    16px on a phone, and only on a phone.

    The base control size is 14px, which is right for a dense operator UI on a
    desktop -- but mobile Safari zooms the whole viewport whenever a focused
    control's font-size is under 16px, and the viewport meta deliberately does
    not set maximum-scale. So every field tap on the first-run screens jumped
    the 360px page to roughly 410px effective width and ran the card off both
    edges, once per field. Height comes from the space scale, so nothing
    reflows; only the glyphs grow.
  */
  @media (max-width: 768px) {
    textarea {
      font-size: var(--z-text-lg);
    }
  }
</style>

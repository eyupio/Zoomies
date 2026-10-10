<!-- The full logo and large emblem share the approved running spaniel artwork.
     The original paw/swish stays legible at navigation sizes. -->
<script lang="ts">
  interface Props {
    variant?: 'mark' | 'wordmark' | 'full' | 'lockup';
    size?: number;
    label?: string;
    inverse?: boolean;
    class?: string;
  }
  const {
    variant = 'full',
    size = 24,
    label = 'Zoomies',
    inverse = false,
    class: klass = '',
  }: Props = $props();
  const asset = $derived(variant === 'lockup' ? 'logo' : size > 64 ? 'mark' : 'paw-swish');
  const edge = $derived(variant === 'lockup' ? Math.max(220, Math.round(size * 3.1)) : size);
  const src = (colour: string) =>
    `/brand/${asset}-${colour}${asset === 'paw-swish' ? '.png' : '-transparent.svg'}`;
</script>

<span
  class="logo {variant} {klass}"
  class:inverse
  role={label ? 'img' : undefined}
  aria-label={label || undefined}
  aria-hidden={label ? undefined : 'true'}
>
  {#if variant !== 'wordmark'}
    <span class="art" style="--edge: {edge}px">
      <img class="black" src={src('black')} width={edge} height={edge} alt="" decoding="async" />
      <img class="white" src={src('white')} width={edge} height={edge} alt="" decoding="async" />
    </span>
  {/if}
  {#if variant === 'full' || variant === 'wordmark'}
    <span class="wordmark" style="--word-h: {Math.round(size * 0.62)}px"></span>
  {/if}
</span>

<style>
  .logo {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    color: var(--z-text);
    line-height: 0;
  }
  .logo.lockup {
    display: flex;
    width: 100%;
    justify-content: center;
  }
  .art {
    display: block;
    width: min(100%, var(--edge));
    aspect-ratio: 1;
  }
  .art img {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: contain;
  }
  .art .white {
    display: none;
  }
  :global(:root[data-theme='dark']) .black {
    display: none;
  }
  :global(:root[data-theme='dark']) .white {
    display: block;
  }
  @media (prefers-color-scheme: dark) {
    :global(:root:not([data-theme='light'])) .black {
      display: none;
    }
    :global(:root:not([data-theme='light'])) .white {
      display: block;
    }
  }
  .logo.inverse .black {
    display: none;
  }
  .logo.inverse .white {
    display: block;
  }
  .wordmark {
    display: block;
    height: var(--word-h);
    aspect-ratio: 1128 / 226;
    background: currentColor;
    mask: url('/brand/wordmark-black-transparent.svg') left center / contain no-repeat;
  }
</style>

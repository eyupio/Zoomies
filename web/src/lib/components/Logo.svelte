<!--
  The Zoomies logo system.

  The v2.1 hierarchy is deliberate:

  * lockup is the unchanged primary full logo, never below 220px wide;
  * mark at 48px and above is the secondary head/swish;
  * mark below 48px is the paw/swish, the official smallest-size shorthand.

  All artwork is the supplied white reverse on Zoomies Black. Nothing is
  recoloured, cropped or reconstructed in CSS.

  See docs/brand.md.
-->
<script lang="ts">
  interface Props {
    /** mark: the dog alone. wordmark: the word alone. full: side by side. lockup: stacked, for sign-in. */
    variant?: 'mark' | 'wordmark' | 'full' | 'lockup';
    /** The mark's edge length in pixels. The wordmark scales with it. */
    size?: number;
    /** Rendered as the accessible name. Set it to "" inside an already-labelled link. */
    label?: string;
    class?: string;
  }

  const { variant = 'full', size = 24, label = 'Zoomies', class: klass = '' }: Props = $props();

  const markSrc = $derived(
    size >= 48 ? '/brand/head-swish-white.png' : '/brand/paw-swish-white.png',
  );
  const wordmarkRatio = 975 / 250;
  const wordHeight = $derived(Math.round(size * 0.62));
  const lockupWidth = $derived(Math.max(220, Math.round(size * 3.1)));
</script>

<span
  class="logo {variant} {klass}"
  role={label ? 'img' : undefined}
  aria-label={label || undefined}
  aria-hidden={label ? undefined : 'true'}
>
  {#if variant === 'lockup'}
    <span class="lockup-frame" style="--lockup-width: {lockupWidth}px">
      <img
        class="primary"
        src="/brand/logo-white.png"
        width={lockupWidth}
        height={lockupWidth}
        alt=""
        decoding="async"
      />
    </span>
  {:else if variant !== 'wordmark'}
    <span class="chip" style="--chip: {size}px">
      <img src={markSrc} width={size} height={size} alt="" decoding="async" />
    </span>
  {/if}

  {#if variant !== 'mark' && variant !== 'lockup'}
    <span
      class="wordmark"
      style="--mark-src: url('/brand/wordmark-white.png'); --word-h: {wordHeight}px; --word-w: {Math.round(
        wordHeight * wordmarkRatio,
      )}px"
    ></span>
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
  .lockup-frame {
    display: grid;
    width: var(--lockup-width);
    height: var(--lockup-width);
    place-items: center;
    background: var(--z-brand-black);
  }
  .chip {
    display: inline-grid;
    width: var(--chip);
    height: var(--chip);
    place-items: center;
    border-radius: var(--z-radius-md);
    background: var(--z-mark-chip);
  }
  .primary,
  .chip img {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: contain;
  }

  /* The wordmark is a mask over currentColor, so it inherits the theme's text
     colour rather than needing a light and a dark copy. */
  .wordmark {
    display: block;
    width: var(--word-w);
    height: var(--word-h);
    background-color: currentColor;
    -webkit-mask-image: var(--mark-src);
    mask-image: var(--mark-src);
    -webkit-mask-repeat: no-repeat;
    mask-repeat: no-repeat;
    -webkit-mask-size: contain;
    mask-size: contain;
    -webkit-mask-position: left center;
    mask-position: left center;
  }
</style>

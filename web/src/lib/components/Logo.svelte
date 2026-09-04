<!--
  The Zoomies mark and wordmark.

  Two different techniques, for a reason each:

  * The mark is monochrome line art with white outlines, drawn to sit on a dark
    ground. Only about two per cent of its pixels are fully opaque -- it is
    outlines, not a silhouette -- so recolouring or inverting it does not work.
    The brand guide sanctions the white knockout over a dark surface, so that is
    what this does: the supplied asset on a small Zoomies Black chip. One asset,
    correct in both themes, legible at 24px, circular shape intact.

  * The wordmark IS a solid shape, so it is applied as a CSS mask over
    `currentColor`. That gives a crisp, correctly themed wordmark from a single
    white PNG, instead of shipping a light and a dark copy and swapping them.

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

  // The lock-up uses the full wordmark, which carries the descriptor line; the
  // nav uses the compact one, where that line would be too small to read.
  const wordmarkSrc = $derived(
    variant === 'lockup' ? '/brand/wordmark-white.png' : '/brand/wordmark-compact-white.png',
  );
  const wordmarkRatio = $derived(variant === 'lockup' ? 320 / 82 : 260 / 44);
  const wordHeight = $derived(
    variant === 'lockup' ? Math.round(size * 0.34) : Math.round(size * 0.62),
  );
</script>

<span
  class="logo {variant} {klass}"
  role={label ? 'img' : undefined}
  aria-label={label || undefined}
  aria-hidden={label ? undefined : 'true'}
>
  {#if variant !== 'wordmark'}
    <span class="chip" style="--chip: {size}px">
      <img
        src="/brand/mark-white.png"
        srcset="/brand/mark-white.png 1x, /brand/mark-white@2x.png 2x"
        width={size}
        height={size}
        alt=""
        decoding="async"
      />
    </span>
  {/if}

  {#if variant !== 'mark'}
    <span
      class="wordmark"
      style="--mark-src: url({wordmarkSrc}); --word-h: {wordHeight}px; --word-w: {Math.round(
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
  .logo.lockup {
    flex-direction: column;
    gap: var(--z-space-4);
  }

  .chip {
    display: inline-grid;
    place-items: center;
    /* The mark's own artwork already carries generous internal margin, so the
       chip only needs a hairline of breathing room around it. */
    padding: calc(var(--chip) * 0.06);
    border-radius: var(--z-radius-md);
    background: var(--z-mark-chip);
  }
  .logo.lockup .chip {
    border-radius: var(--z-radius-lg);
  }
  .chip img {
    display: block;
    width: var(--chip);
    height: var(--chip);
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
  .logo.lockup .wordmark {
    -webkit-mask-position: center;
    mask-position: center;
  }
</style>

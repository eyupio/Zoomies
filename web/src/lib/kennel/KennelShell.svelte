<!--
  A Kennel Club page with the section's rail beside it. The page keeps its own
  header: the shell is only the place the rail and the page share, as the one in
  Settings is, so a page is still the one component that says what it holds.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import KennelRail from './KennelRail.svelte';
  import type { KennelPageId } from './pages';

  interface Props {
    current: KennelPageId;
    children: Snippet;
  }

  let { current, children }: Props = $props();
</script>

<div class="section">
  <KennelRail {current} />
  <div class="page">{@render children()}</div>
</div>

<style>
  .section {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-8);
  }
  /* No maximum width: these pages are tables and tiles, and Settings' are forms. */
  .page {
    flex: 1;
    min-width: 0;
  }
  @media (max-width: 1180px) {
    .section {
      flex-direction: column;
      align-items: stretch;
      gap: var(--z-space-4);
    }
  }
</style>

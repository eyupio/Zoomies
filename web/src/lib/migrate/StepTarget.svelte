<!--
  Step one: whose repositories.

  A migration reads and writes through one GitHub App installation, so this is
  the first question and there is nothing sensible to default it to when a
  controller has more than one.
-->
<script lang="ts">
  import type { Installation } from '$lib/api/types';
  import RadioGroup from '$lib/components/RadioGroup.svelte';

  interface Props {
    installations: readonly Installation[];
    selected?: string;
  }

  let { installations, selected = $bindable('') }: Props = $props();

  const options = $derived(
    installations.map((i) => ({
      value: i.id ?? '',
      label: i.target ?? '(unnamed)',
      description: i.healthy
        ? `${i.target_type === 'repo' ? 'One repository' : 'An organisation'}${i.pool_count ? `, ${i.pool_count} ${i.pool_count === 1 ? 'pool' : 'pools'}` : ', no pools yet'}`
        : `Its last credential check failed: ${i.last_error ?? 'unknown'}`,
      disabled: !i.healthy,
    })),
  );
</script>

<p class="lede">
  Zoomies will read this installation's repositories, and — once you have seen the diff — open a
  pull request on each one you pick. Nothing is written before that.
</p>

<RadioGroup bind:value={selected} {options} name="installation" legend="Installation" />

<style>
  .lede {
    margin: 0 0 var(--z-space-4);
    max-width: 70ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
</style>

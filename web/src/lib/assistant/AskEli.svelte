<script lang="ts">
  import { MessageCircle } from '@lucide/svelte';
  import Button from '$lib/components/Button.svelte';
  import { session } from '$lib/state/session.svelte';
  import { eli } from './eli.svelte';
  import type { EliContext } from './prompts';

  let { context }: { context: EliContext } = $props();
</script>

{#if session.can('viewer')}
  <Button
    variant="ghost"
    size="sm"
    icon={MessageCircle}
    title="Share these displayed details with Eli and ask for guidance"
    onclick={() => eli.ask(context)}>Ask Eli</Button
  >
{/if}

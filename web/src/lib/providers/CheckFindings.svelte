<!--
  What a provider's preflight found, one finding at a time: the title says what
  is true, the detail says why it matters and (for a provider that can) what it
  offered instead, and the fix says what to change.

  It is a component because two places read the same result: the detail page
  straight after Check, and the card long after it, from the copy the
  controller keeps. A title on its own ("no bridge called vmbr0") is the one
  part that tells an operator nothing they can act on.
-->
<script lang="ts">
  import type { ProviderCheck } from '$lib/api/types';
  import { severityStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import RemedyText from '$lib/components/RemedyText.svelte';

  interface Props {
    findings: ProviderCheck['findings'] | undefined;
    class?: string;
  }

  let { findings, class: className = '' }: Props = $props();
</script>

<ul class="findings {className}">
  {#each findings ?? [] as finding (finding.code)}
    <li>
      <Badge status={severityStatus(finding.severity)} size="sm" />
      <div>
        <p class="title">{finding.title}</p>
        {#if finding.setting}<p class="setting">Setting: <code>{finding.setting}</code></p>{/if}
        {#if finding.detail}<p class="detail"><RemedyText text={finding.detail} /></p>{/if}
        {#if finding.fix}<p class="detail fix"><RemedyText text={finding.fix} /></p>{/if}
      </div>
    </li>
  {/each}
</ul>

<style>
  .findings {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
  }
  div {
    min-width: 0;
  }
  .title {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .setting,
  .detail {
    margin: var(--z-nudge-1) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .fix {
    color: var(--z-text);
  }
  code {
    font-family: var(--z-font-mono);
  }
</style>

<!--
  A host's update, on its card and on its page.

  It stands outside the folded copyable command and outside any summary: a
  control inside a <summary> is a control that is also a disclosure toggle, and
  pressing the wrong half of it is how a host gets updated by accident. The
  command stays beneath for everyone who can see it; this is the one-press way
  for an administrator.

  What it shows is the controller's, read off the host and never kept here, so
  an update in flight is still in flight after a reload. The state is a word as
  well as a colour, the reason is text beside the button rather than a tooltip
  (a tooltip is not there on a phone), and the polite region says each change
  once, in words that never claim the update worked before the host says so.
-->
<script lang="ts">
  import { CircleArrowUp } from '@lucide/svelte';
  import type { Host } from '$lib/api/types';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import { hostUpdateWords, targetTag } from './update';

  interface Props {
    host: Host;
    /** Ask for the confirmation. The row never posts anything itself. */
    onupdate: (host: Host) => void;
  }

  let { host, onupdate }: Props = $props();

  const name = $derived(host.name || host.id || 'this host');
  const words = $derived(hostUpdateWords(host));
  const tag = $derived(targetTag(host));
  const label = $derived(
    words?.action === 'Try again'
      ? `Try again to update ${name}${tag ? ` to ${tag}` : ''}`
      : `Update ${name}${tag ? ` to ${tag}` : ''}`,
  );
</script>

{#if words}
  <!-- Focusable by script only: after a press the button is gone, and the
       confirmation hands focus back to a place that no longer exists. -->
  <section
    class="update"
    class:flight={words.inFlight}
    id="host-{host.id}-update"
    aria-label="Agent update for {name}"
    tabindex="-1"
  >
    <div class="head">
      <Badge tone={words.tone} label={words.label} size="sm" dot={false} />
      {#if words.offered}
        <Button
          size="sm"
          variant="secondary"
          icon={CircleArrowUp}
          ariaLabel={label}
          disabled={!words.canPress}
          onclick={() => onupdate(host)}>{words.action}</Button
        >
      {/if}
    </div>
    <!-- Text from the controller, or from a helper on the host that it relayed:
         interpolated as text, so it can never be markup. -->
    <p class="sentence" id="host-{host.id}-update-sentence">{words.sentence}</p>
    <!-- Always in the page, empty until there is something to say, because a
         region that appears with its words already in it is not reliably read
         out. -->
    <p class="sr-only" role="status" aria-live="polite">{words.live}</p>
  </section>
{/if}

<style>
  .update {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding: var(--z-space-2) var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    min-width: 0;
  }
  .update.flight {
    border-color: var(--z-accent-border);
    background: var(--z-accent-subtle);
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-2);
  }
  .sentence {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
</style>

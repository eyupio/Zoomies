<!--
  Updates: which release the update mode would take, and why.

  The controller works the status out and this reads it, so every figure and
  every sentence here is one the API gives too. The mode and the soak come from
  the same document as the release they explain, because an administrator is not
  sent the `updates.*` rows and the status carries them beside the sentence that
  depends on them.

  It is written in the conditional because it only reads: the line says what the
  mode would do, the controller's sentence under it says why, as it was given,
  and nothing on the page moves a release. The mode is text, not a control, for
  the same reason -- it is changed where the setting is, by the role that may.
-->
<script lang="ts">
  import { ExternalLink } from '@lucide/svelte';
  import { session } from '$lib/state/session.svelte';
  import { updates } from '$lib/state/updates.svelte';
  import {
    buildText,
    describeMode,
    modeLabel,
    releaseHref,
    soakNote,
    soakText,
    targetLine,
    updateState,
  } from '$lib/updates/words';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  const status = $derived(updates.status);
  // Both are worked out when a status arrives, and not on a timer: that is when
  // the controller's sentence under them was, so the two count from one moment.
  const badge = $derived(status ? updateState(status) : null);
  const line = $derived(status ? targetLine(status) : '');
  const href = $derived(releaseHref(status?.latest?.url));
  const canAdmin = $derived(session.can('admin'));

  $effect(() => updates.follow());
</script>

<PageHeader
  title="Updates"
  subtitle="Which release the update mode would take, and when."
  onrefresh={() => updates.refresh()}
/>

<LoadingBoundary
  loading={updates.loading && !status}
  error={status ? null : updates.error}
  onretry={() => void updates.refresh()}
>
  {#snippet skeleton()}
    <div class="stack">
      <Skeleton lines={3} />
      <Skeleton lines={3} />
    </div>
  {/snippet}

  {#if status}
    <div class="stack">
      {#if updates.error}
        <div class="notice" role="status">
          <p>
            The last refresh did not get through, so this is what was last received.
            <Button size="sm" variant="ghost" onclick={() => void updates.refresh()}>
              Try again
            </Button>
          </p>
        </div>
      {/if}

      <Panel title="What an update would take" class="updates-take">
        {#snippet actions()}
          {#if badge}<Badge tone={badge.tone} label={badge.label} dot={false} />{/if}
        {/snippet}

        {#if line}<p class="line">{line}</p>{/if}
        <p class="reason">{status.reason}</p>

        {#if status.latest || status.checked_at}
          <dl class="facts">
            {#if status.latest}
              <dt>Newest release</dt>
              <dd>
                {#if href}
                  <a class="release" {href} target="_blank" rel="noopener noreferrer">
                    <span class="mono">{status.latest.tag}</span>
                    <ExternalLink size={13} aria-hidden="true" />
                    <span class="sr-only"> (opens in a new tab)</span>
                  </a>
                {:else}
                  <span class="mono">{status.latest.tag}</span>
                {/if}
              </dd>
              <dt>Published</dt>
              <dd><RelativeTime value={status.latest.published_at} /></dd>
            {/if}
            {#if status.checked_at}
              <dt>Last checked</dt>
              <dd><RelativeTime value={status.checked_at} /></dd>
            {/if}
          </dl>
        {/if}
      </Panel>

      <Panel title="Update mode">
        <dl class="facts">
          <dt>Mode</dt>
          <dd>
            <Badge tone="neutral" label={modeLabel(status.mode)} dot={false} />
            <p class="detail">{describeMode(status.mode)}</p>
          </dd>
          <dt>Soak</dt>
          <dd>
            {soakText(status.soak)}
            <p class="detail">{soakNote(status.mode)}</p>
          </dd>
        </dl>
        <p class="note">
          The platform role changes both, as the settings <code>updates.mode</code> and
          <code>updates.soak</code>{#if canAdmin}, on
            <a href="/settings/configuration?setting=updates.mode">the Configuration page</a>{/if}.
        </p>
      </Panel>

      <Panel title="This controller">
        <dl class="facts">
          <dt>Version</dt>
          <dd class="mono">{status.running.version}</dd>
          <dt>Build</dt>
          <dd>{buildText(status.running.release)}</dd>
        </dl>
      </Panel>
    </div>
  {/if}
</LoadingBoundary>

<style>
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
  }
  .notice {
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-accent-border);
    border-radius: var(--z-radius-md);
    background: var(--z-accent-subtle);
    color: var(--z-text);
    font-size: var(--z-text-sm);
  }
  .notice p {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
  }
  .line {
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  /* The controller's sentence is one or two and says whatever the state needs,
     so no line is sized for it: prose wraps at a readable measure. */
  .reason,
  .detail,
  .note {
    max-width: 72ch;
  }
  .reason {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .facts {
    display: grid;
    grid-template-columns: minmax(0, 11rem) minmax(0, 1fr);
    gap: var(--z-space-3) var(--z-space-4);
    margin: 0;
    font-size: var(--z-text-base);
  }
  .reason + .facts {
    margin-top: var(--z-space-4);
    padding-top: var(--z-space-4);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  dt {
    color: var(--z-text-muted);
  }
  dd {
    margin: 0;
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .detail {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  .note {
    margin: var(--z-space-4) 0 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  .release {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    color: var(--z-accent);
    text-decoration: none;
  }
  .release:hover {
    color: var(--z-accent-hover);
    text-decoration: underline;
    text-underline-offset: var(--z-underline-offset);
  }
  /* A key above its value, as the other Settings pages' rows stack on a phone. */
  @media (max-width: 768px) {
    .facts {
      grid-template-columns: minmax(0, 1fr);
      gap: var(--z-space-1);
    }
    dd + dt {
      margin-top: var(--z-space-2);
    }
  }
</style>

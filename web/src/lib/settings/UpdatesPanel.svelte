<!--
  Updates: which release the update mode would take, and why.

  The controller works the status out and this reads it, so every figure and
  every sentence here is one the API gives too. The mode and the soak come from
  the same document as the release they explain, because an administrator is not
  sent the `updates.*` rows and the status carries them beside the sentence that
  depends on them.

  What the mode would take is written in the conditional, because the line says
  what the mode would do and the controller's sentence under it says why, as it
  was given. The mode is text, not a control: it is changed where the setting is,
  by the role that may.

  The one thing here that acts is the platform role's Update button for this
  controller. Everything about it is read from the status and not remembered by
  the page: whether an attempt is open, how the last one ended and whether the
  button is offered all come from `controller` and `helper`, so a reload, a
  reconnecting stream or a second tab shows the same. The page never says an
  update worked before the controller has: success is the attempt closed as
  succeeded, or the build it reports being the release asked for.
-->
<script lang="ts">
  import { ExternalLink } from '@lucide/svelte';
  import { ApiError } from '$lib/api/client';
  import { authFailureText, sentence } from '$lib/errors';
  import { session } from '$lib/state/session.svelte';
  import { updates } from '$lib/state/updates.svelte';
  import UpdateControllerDialog from '$lib/updates/UpdateControllerDialog.svelte';
  import {
    attemptWords,
    buildText,
    controllerOffer,
    describeMode,
    modeLabel,
    modeSettingHref,
    releaseHref,
    soakNote,
    soakText,
    targetLine,
    UPDATE_RESTART,
    updateState,
  } from '$lib/updates/words';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import RestartWait from './RestartWait.svelte';

  /*
    How long the controller is given to answer again once this page has lost it.
    An upgrade waits up to thirty minutes for a controller that is migrating a
    large database, so a page that gave up in two would be calling a slow start a
    failed one. After this it stops promising and says where to look; the attempt
    itself is the controller's to close, at ninety minutes at the latest.
  */
  const RESTART_LIMIT_S = 900;

  const status = $derived(updates.status);
  // Both are worked out when a status arrives, and not on a timer: that is when
  // the controller's sentence under them was, so the two count from one moment.
  const badge = $derived(status ? updateState(status) : null);
  const line = $derived(status ? targetLine(status) : '');
  const href = $derived(releaseHref(status?.latest?.url));
  const settingHref = $derived(modeSettingHref((role) => session.can(role)));

  const canUpdate = $derived(session.can('platform'));
  const offer = $derived(status ? controllerOffer(status, canUpdate) : null);
  const attempt = $derived(status?.controller ?? null);
  const attemptText = $derived(
    status && attempt ? attemptWords(attempt, status.running.version) : null,
  );

  let confirming = $state(false);
  // The release the button named when it was pressed. The request carries this
  // one and the dialog names it, so a newer release arriving while the dialog is
  // open cannot change what the operator agreed to.
  let asked = $state('');
  // Why the last press was refused, in the controller's words. It belongs to the
  // press and not to the status: a refusal opens no attempt, so nothing in the
  // status would carry it.
  let refusal = $state('');

  // The page has lost the controller while an update is open: what a restart
  // looks like from here. `connecting` is not that, it is how every page load
  // begins, so only a stream that was up and went is taken for one. Held so that
  // the restart state stays up until the stream is back, however briefly it
  // flickers, and dropped as soon as no attempt is open.
  let restarting = $state(false);
  $effect(() => {
    if (!attemptText?.open) {
      restarting = false;
    } else if (updates.stream === 'reconnecting' || updates.stream === 'offline') {
      restarting = true;
    }
  });

  let progress = $state<HTMLElement | null>(null);
  let focusProgress = $state(false);

  // After a press the button is gone and the dialog's opener with it, so focus
  // would fall to the page. It goes to the state that replaced the button. The
  // dialog gives focus back a frame or two after it closes, and finding its
  // opener gone sends it to the page heading; so this asks after those frames,
  // and once more a moment later if focus has still been left on the page.
  function landOn(target: HTMLElement): void {
    requestAnimationFrame(() =>
      requestAnimationFrame(() => {
        target.focus();
        setTimeout(() => {
          const held = document.activeElement;
          if (!held || held === document.body || held.id === 'page-heading') target.focus();
        }, 250);
      }),
    );
  }

  $effect(() => {
    if (focusProgress && !confirming && offer?.kind === 'in-flight' && progress) {
      focusProgress = false;
      landOn(progress);
    }
  });

  function ask(tag: string): void {
    refusal = '';
    asked = tag;
    confirming = true;
  }

  async function start(tag: string): Promise<boolean> {
    // Before the status lands, because the effect that moves focus runs as soon
    // as it does.
    focusProgress = true;
    try {
      await updates.updateController(tag);
      return true;
    } catch (cause) {
      focusProgress = false;
      // Closed either way: the dialog has nothing more to ask, and the sentence
      // is on the page where the button was.
      refusal =
        cause instanceof ApiError && cause.status >= 400 && cause.status < 500
          ? sentence(cause.message)
          : authFailureText(cause);
      return true;
    }
  }

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

      <Panel title="What an update would take">
        {#snippet actions()}
          {#if badge}<Badge tone={badge.tone} label={badge.label} dot={false} />{/if}
        {/snippet}

        {#if line}<p class="line">{line}</p>{/if}
        <p class="reason" id="update-reason">{status.reason}</p>

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

      <Panel title="Update this controller">
        {#if attempt && attemptText}
          {#if restarting && attemptText.open}
            <RestartWait
              reason={`Updating from ${attempt.from} to ${attempt.to}`}
              copy={UPDATE_RESTART}
              begin="starting"
              returnTo="/settings/updates"
              startLimit={RESTART_LIMIT_S}
              answering={() => updates.stream === 'live'}
            />
          {:else}
            <section
              class="attempt"
              data-state={attemptText.open ? 'requested' : attempt.state}
              aria-labelledby="update-attempt-title"
              tabindex="-1"
              bind:this={progress}
            >
              <div class="attempt-head">
                <Badge tone={attemptText.tone} label={attemptText.label} dot={false} />
                <h3 id="update-attempt-title">{attemptText.title}</h3>
              </div>
              <p class="reason">{attemptText.detail}</p>
              {#if attempt.error && !attemptText.open && attempt.state !== 'timed_out' && attempt.state !== 'succeeded'}
                <!-- Text from the update helper or the controller: shown as it came, never as markup. -->
                <p class="helper-says">{attempt.error}</p>
              {/if}
              {#if attemptText.lookAt}<p class="detail">{attemptText.lookAt}</p>{/if}
              <dl class="facts">
                <dt>Asked</dt>
                <dd>
                  <RelativeTime value={attempt.requested_at} />
                  {attempt.trigger === 'auto' ? 'by the update mode' : 'by a person'}
                </dd>
                {#if attempt.finished_at}
                  <dt>Ended</dt>
                  <dd><RelativeTime value={attempt.finished_at} /></dd>
                {/if}
              </dl>
            </section>
          {/if}
        {/if}

        <!-- Said politely and from a region that is always there, because one that appears with its words in it is not reliably read out. -->
        <p class="sr-only" role="status" aria-live="polite">
          {restarting && attemptText?.open
            ? UPDATE_RESTART.titles.starting
            : (attemptText?.title ?? '')}
        </p>

        {#if offer?.kind === 'offer'}
          <div class="act">
            {#if refusal}<p class="refusal" role="alert">{refusal}</p>{/if}
            <Button variant="primary" onclick={() => ask(offer.tag)}>Update to {offer.tag}</Button>
          </div>
        {:else if offer?.kind === 'none'}
          <div class="act">
            {#if refusal}<p class="refusal" role="alert">{refusal}</p>{/if}
            <p class="reason" id="update-offer-reason">{offer.sentence}</p>
            {#if offer.installCommand}
              <CopyButton
                value={offer.installCommand}
                label="Copy the install command"
                showValue
                showLabel
              />
            {:else if offer.upgradeCommand}
              <CopyButton
                value={offer.upgradeCommand}
                label="Copy the upgrade command"
                showValue
                showLabel
              />
            {/if}
          </div>
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
          <code>updates.soak</code>{#if settingHref}, on
            <a href={settingHref}>the Configuration page</a>{/if}.
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

{#if status}
  <UpdateControllerDialog
    bind:open={confirming}
    tag={asked}
    running={status.running.version}
    onconfirm={() => start(asked)}
  />
{/if}

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
  .attempt {
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .attempt[data-state='failed'],
  .attempt[data-state='timed_out'] {
    border-color: var(--z-danger-border);
    background: var(--z-danger-subtle);
  }
  .attempt-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
    margin-bottom: var(--z-space-2);
  }
  .attempt-head h3 {
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .attempt .facts {
    margin-top: var(--z-space-3);
  }
  .helper-says {
    max-width: var(--z-measure-prose);
    margin: var(--z-space-2) 0 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .act {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-3);
  }
  .attempt + .sr-only + .act {
    margin-top: var(--z-space-4);
  }
  .refusal {
    max-width: var(--z-measure-prose);
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-danger);
    overflow-wrap: anywhere;
  }
  /* The controller's sentence is one or two and says whatever the state needs,
     so no line is sized for it: prose wraps at a readable measure. */
  .reason,
  .detail,
  .note {
    max-width: var(--z-measure-prose);
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
    grid-template-columns: var(--z-facts-columns);
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

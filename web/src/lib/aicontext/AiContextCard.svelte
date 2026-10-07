<!--
  One repository's AI Context: its state, what it last verified, why a run failed,
  the actions that fit where it is, the notes, and the instructions to hand an
  assistant.

  It is the card the AI Context page lists and the card a repository's own page
  shows in its AI Context tab, so the two cannot drift. It calls the same
  endpoints and applies the same gates on both, because the gates are the API's
  and not the page's: what it offers is decided by the record it is given.
-->
<script lang="ts">
  import { recheckAIContext, regenerateAIContext } from '$lib/api/client';
  import type { AIContextRepository } from '$lib/api/types';
  import { formatAbsolute } from '$lib/format';
  import { aiContextStatus } from '$lib/status';
  import { toasts } from '$lib/state/toasts.svelte';
  import type { AiContextItem, KnownInstallation } from '$lib/aicontext/types';
  import NotesPanel from '$lib/aicontext/NotesPanel.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';

  interface Props {
    item: AiContextItem;
    /** The installations this person may configure, so one is named and not shown as an ID. */
    installations: readonly KnownInstallation[];
    /** Whether to head the card with the repository's name; a page already about it has no need. */
    named?: boolean;
    /** Told the record the controller answered with after a recheck or a regenerate. */
    onchange?: (updated: AIContextRepository) => void;
  }

  let { item, installations, named = true, onchange }: Props = $props();

  let checking = $state(false);
  let regenerating = $state(false);
  let checkFailure = $state<{ cause: unknown; retry: 'recheck' | 'regenerate' } | null>(null);

  type Diagnosis = NonNullable<AIContextRepository['diagnosis']>;

  // What Zoomies will do about a failed run, in a sentence. "Will not" covers both
  // a cause no run can fix and one it has already tried as often as it will:
  // either way it is up to the person, and Regenerate is how to try once they have.
  function retryText(retry: Diagnosis['retry']): string {
    if (retry?.automatic && retry.next_at) {
      const left = retry.attempts_left;
      return `Zoomies will start the workflow again from ${formatAbsolute(retry.next_at)} (${left} ${left === 1 ? 'attempt' : 'attempts'} left for this commit).`;
    }
    return 'Zoomies will not start the workflow again by itself. Use Regenerate once this is fixed.';
  }

  // The run's address comes from GitHub, but a link in the page is only ever
  // followed if it is https.
  function runLink(url: string | undefined): string | undefined {
    return url?.startsWith('https://') ? url : undefined;
  }

  async function recheck() {
    checking = true;
    checkFailure = null;
    try {
      onchange?.(await recheckAIContext(item.id));
    } catch (cause) {
      checkFailure = { cause, retry: 'recheck' };
    } finally {
      checking = false;
    }
  }

  // Regenerating asks GitHub to run the workflow; it does not verify anything, so
  // the card keeps showing the old state until the next recheck says otherwise.
  async function regenerate() {
    regenerating = true;
    checkFailure = null;
    try {
      onchange?.(await regenerateAIContext(item.id));
      toasts.success(
        'Workflow started',
        'Recheck context once the run finishes; it usually takes a few minutes.',
      );
    } catch (cause) {
      checkFailure = { cause, retry: 'regenerate' };
    } finally {
      regenerating = false;
    }
  }
</script>

<section class="repository" aria-label={item.full_name}>
  <div class="heading">
    {#if named}<h2>{item.full_name}</h2>{/if}
    <div class="badges">
      <Badge
        status={aiContextStatus(
          'config' in item
            ? item.freshness?.state
              ? item.freshness.state
              : item.available
                ? 'available'
                : item.setup_state === 'awaiting_merge'
                  ? 'awaiting_merge'
                  : item.setup_state === 'pending'
                    ? 'working'
                    : 'draft'
            : 'available',
        )}
        label={'config' in item
          ? item.config.disabled
            ? 'Removed from Zoomies'
            : undefined
          : 'Shared with you'}
      />
      {#if 'config' in item && item.workflow_outdated && !item.config.disabled}<Badge
          tone="accent"
          label="Workflow out of date"
          title="The workflow was written by an earlier Zoomies release. It still works; Reinstall / repair moves it to the current generator."
        />{/if}
    </div>
  </div>
  {#if 'config' in item}
    <dl>
      <div>
        <dt>Source branch</dt>
        <dd>{item.config.source_branch}</dd>
      </div>
      <div>
        <dt>Output</dt>
        <dd>
          {item.config.destination === 'both'
            ? 'Repository and Zoomies'
            : item.config.destination === 'repository'
              ? 'Repository'
              : 'Zoomies'}
        </dd>
      </div>
      <div>
        <dt>Installation</dt>
        <dd>
          {installations.find((i) => i.id === item.repository.installation_id)?.target ??
            item.repository.installation_id}
        </dd>
      </div>
    </dl>
    {#if item.freshness}
      <dl>
        <div>
          <dt>Last checked</dt>
          <dd>{formatAbsolute(item.freshness.checked_at)}</dd>
        </div>
        {#if item.freshness.published_commit}<div>
            <dt>Last verified commit</dt>
            <dd><code>{item.freshness.published_commit.slice(0, 12)}</code></dd>
          </div>{/if}
      </dl>
      {#if item.diagnosis}
        <div class="diagnosis" role="group" aria-label="Why the last workflow run failed">
          <h3>{item.diagnosis.title}</h3>
          <p>{item.diagnosis.detail}</p>
          <p><strong>What to do.</strong> {item.diagnosis.fix}</p>
          <p class="muted">{retryText(item.diagnosis.retry)}</p>
          {#if runLink(item.diagnosis.run_url)}<Button
              size="sm"
              newTab
              href={runLink(item.diagnosis.run_url)}>Open the failed run</Button
            >{/if}
        </div>
      {:else if item.freshness.failure}<p class="verification-failure">
          {item.freshness.failure}
        </p>{/if}
    {/if}
    {#if checkFailure}<ErrorState
        error={checkFailure.cause}
        title={checkFailure.retry === 'regenerate'
          ? 'The workflow could not be started'
          : 'Verification could not finish'}
        onretry={() => (checkFailure?.retry === 'regenerate' ? regenerate() : recheck())}
      />{/if}
    <div class="actions">
      {#if item.setup_state === 'awaiting_merge'}<Button
          size="sm"
          loading={checking}
          disabled={checking || regenerating}
          onclick={() => recheck()}>Recheck context</Button
        >{/if}
      {#if item.setup_state === 'awaiting_merge' && item.freshness && item.freshness.state !== 'awaiting_merge'}<Button
          size="sm"
          loading={regenerating}
          disabled={regenerating || checking}
          onclick={() => regenerate()}>Regenerate</Button
        >{/if}
      {#if item.setup_pr_url}<Button size="sm" newTab href={item.setup_pr_url}>Open setup PR</Button
        >{/if}
      <Button size="sm" href="/kennel/ai-context/setup?draft_id={encodeURIComponent(item.id)}"
        >{item.setup_state ? 'View setup' : 'Resume setup'}</Button
      >
      {#if item.setup_state}
        {#each ['reinstall', 'amend', 'remove'] as mode (mode)}
          <Button
            size="sm"
            variant={(mode === 'reinstall' &&
              (item.workflow_outdated || item.diagnosis?.action === 'repair')) ||
            (mode === 'amend' && item.diagnosis?.action === 'exclusions')
              ? 'primary'
              : 'secondary'}
            href="/kennel/ai-context/setup?draft_id={encodeURIComponent(item.id)}&mode={mode}"
            >{mode === 'reinstall'
              ? 'Reinstall / repair'
              : mode === 'amend'
                ? 'Amend'
                : 'Remove'}</Button
          >
        {/each}
      {/if}
    </div>
  {:else}<Button size="sm" href="/settings/connections">Choose connection access</Button>{/if}
  {#if item.instructions}
    <div class="actions assistant-prompt">
      <CopyButton value={item.instructions} label="Copy AI instructions" showLabel />
    </div>
  {/if}
  {#if !('config' in item) || item.available}<NotesPanel repositoryId={item.id} />{/if}
  <details class="assistant-guidance">
    <summary>AI instructions and README badge</summary>
    <p>
      Paste the copied instructions into your AI conversation. Repository output works through
      GitHub access without MCP; Zoomies-only output needs a connected assistant. The instructions
      explain which route is available and how to check freshness.
    </p>
    <div class="actions">
      {#if item.badge_markdown}<CopyButton
          value={item.badge_markdown}
          label="Copy badge Markdown"
          showLabel
        />{/if}
    </div>
    {#if item.badge_markdown}
      <p class="muted">
        The Zoomies AI Context badge shows workflow status, not context freshness. Private badges
        require GitHub access.
      </p>
    {/if}
    {#if item.instructions}<details>
        <summary>Preview AI instructions</summary>
        <pre>{item.instructions}</pre>
      </details>{/if}
  </details>
</section>

<style>
  .assistant-prompt {
    margin-top: var(--z-space-4);
  }
  .assistant-guidance {
    margin-top: var(--z-space-4);
  }
  .assistant-guidance summary {
    cursor: pointer;
  }
  .assistant-guidance pre {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-size: var(--z-text-sm);
  }
  .badges {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
  }
  .diagnosis {
    margin-bottom: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    background: var(--z-surface-sunken);
    border-left: var(--z-border-width-rail) solid var(--z-danger-border);
    border-radius: var(--z-radius-sm);
    font-size: var(--z-text-sm);
  }
  .diagnosis h3 {
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-base);
  }
  .diagnosis p {
    margin: 0 0 var(--z-space-2);
  }
  .verification-failure {
    color: var(--z-danger);
    font-size: var(--z-text-sm);
    margin-bottom: var(--z-space-3);
  }
  .repository {
    min-width: 0;
    background: var(--z-surface);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    padding: var(--z-space-5);
    overflow-wrap: anywhere;
  }
  .heading {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
  }
  h2 {
    min-width: 0;
    margin: 0;
    font-size: var(--z-text-base);
  }
  dl {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-5);
    margin: var(--z-space-4) 0;
  }
  dt {
    color: var(--z-text-subtle);
    font-size: var(--z-text-xs);
  }
  dd {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-sm);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--z-space-3);
    flex-wrap: wrap;
  }
  /* Right-aligned buttons that wrap leave each row ragged on the left and
     the first one floating; on a phone they read as a list from the edge. */
  @media (max-width: 768px) {
    .actions {
      justify-content: flex-start;
    }
  }
</style>

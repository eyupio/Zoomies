<script lang="ts">
  import { createKennelGuidancePR, previewKennelGuidance } from '$lib/api/client';
  import type { KennelGuidancePreview, KennelRepository } from '$lib/api/types';
  import Button from '$lib/components/Button.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Finding from '$lib/kennel/Finding.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';

  let { repo }: { repo: KennelRepository } = $props();
  const repositoryId = $derived(repo.id);
  let plan = $state<KennelGuidancePreview | null>(null);
  let busy = $state(false);
  let generation = 0;
  let read: AbortController | undefined;
  const findings = $derived(repo.findings.filter((f) => f.code.startsWith('guidance.')));
  const coverage = $derived(repo.coverage.find((source) => source.source === 'guidance'));
  const enabled = $derived(
    ![
      'guidance.missing',
      'guidance.broken_reference',
      'guidance.duplicated',
      'guidance.unreadable',
    ].every((code) => repo.disabled.includes(code)),
  );

  $effect(() => {
    void repositoryId;
    generation += 1;
    plan = null;
    busy = false;
    return () => read?.abort();
  });

  async function preview(): Promise<void> {
    const current = generation;
    const id = repo.id;
    read?.abort();
    read = new AbortController();
    busy = true;
    plan = null;
    try {
      const next = await previewKennelGuidance(id, read.signal);
      if (current === generation) plan = next;
    } catch (cause) {
      if (current === generation && !read.signal.aborted)
        toasts.fromError(cause, 'Agent guidance could not be previewed');
    } finally {
      if (current === generation) busy = false;
    }
  }

  async function propose(): Promise<void> {
    if (!plan) return;
    const current = generation;
    const id = repo.id;
    busy = true;
    try {
      const next = await createKennelGuidancePR(id, { plan_hash: plan.plan_hash });
      if (current === generation) plan = next;
    } catch (cause) {
      if (current === generation) {
        plan = null;
        toasts.fromError(
          cause,
          'The draft pull request could not be opened; preview again before retrying',
        );
      }
    } finally {
      if (current === generation) busy = false;
    }
  }

  const descriptions = {
    missing: 'No recognised agent guidance found.',
    broken_reference:
      'A local reference is missing or Claude imports form a cycle. Review the file and line.',
    duplicated: 'Claude repeats the root AGENTS.md in full.',
    unreadable: 'This instruction file could not be inspected within the limits.',
  };
</script>

<Panel
  title="Agent guidance"
  description="Instruction files tell coding assistants how to work here. AI Context freshness is checked separately."
>
  <div class="guidance">
    {#if !repo.tracking.tracked}
      <p>This repository is not tracked. Start tracking it to check and propose agent guidance.</p>
    {:else if !enabled}
      <p>
        Agent guidance checks are off. An administrator can enable <code>kennel.agent_guidance</code
        > in Settings. Checks read up to 32 instruction files of 64 KiB each.
      </p>
      {#if session.can('admin')}<Button href="/settings/configuration">Open Settings</Button>{/if}
    {:else}
      {#if coverage?.state !== 'ok'}
        <p>
          {coverage?.reason || 'Agent guidance has not been read yet.'} This is not an all clear.
        </p>
      {:else if findings.length === 0}
        <p>
          No open agent guidance findings. These checks cover structure and references; they do not
          judge the accuracy of prose or commands.
        </p>
      {/if}
      {#each findings as finding (finding.code + finding.subject)}
        <Finding {finding} files={repo.files} />
      {/each}
      {#if session.can('admin')}
        <p>
          Preview safe changes, then open a draft pull request. Review it in GitHub, merge it and
          press Recheck. Existing instructions are preserved; a single supported guidance file is
          sufficient.
        </p>
        <Button loading={busy} onclick={() => void preview()}>Preview guidance changes</Button>
      {:else}
        <p>
          An administrator can preview file contents and propose changes. Findings can be waived
          from the CI tab.
        </p>
      {/if}
    {/if}
    {#if plan && enabled && repo.tracking.tracked && session.can('admin')}
      <h3>Proposed changes</h3>
      {#if plan.issues.length > 0}
        <ul>
          {#each plan.issues as issue, index (index)}
            <li>{descriptions[issue.kind]} {issue.path}{issue.line ? `:${issue.line}` : ''}</li>
          {/each}
        </ul>
      {/if}
      {#each plan.files as file (file.path)}
        <section aria-label={file.path}>
          <h4>{file.path}</h4>
          {#if file.before}
            <details>
              <summary>Current contents</summary>
              <pre>{file.before}</pre>
            </details>
          {:else}<p>New file</p>{/if}
          <details open>
            <summary>Proposed contents</summary>
            <pre>{file.content}</pre>
          </details>
        </section>
      {/each}
      {#if plan.pull_request}
        <p>Draft pull request #{plan.pull_request.number} is open. Merge it, then press Recheck.</p>
        <Button newTab href={plan.pull_request.html_url}>Review draft pull request</Button>
      {:else if plan.files.length > 0}
        <Button variant="primary" loading={busy} onclick={() => void propose()}
          >Open draft pull request</Button
        >
      {:else}
        <p>
          No automatic changes to propose. Review any broken links or import cycles manually. For
          damaged Zoomies-managed context sections, use Reinstall / repair in AI Context.
        </p>
      {/if}
    {/if}
  </div>
</Panel>

<style>
  .guidance {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    align-items: flex-start;
  }
  p,
  h3,
  h4 {
    margin: 0;
  }
  p,
  li {
    font-size: var(--z-text-sm);
  }
  section {
    width: 100%;
    min-width: 0;
  }
  details {
    margin-top: var(--z-space-2);
    width: 100%;
  }
  summary {
    cursor: pointer;
  }
  pre {
    max-height: calc(var(--z-space-16) * 6);
    overflow: auto;
    padding: var(--z-space-3);
    background: var(--z-surface);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-size: var(--z-text-xs);
  }
</style>

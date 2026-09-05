<!--
  Step four: the exact change.

  Everything before this was a guess about what the operator wants. This is the
  diff that is about to appear in somebody else's repository, so it is shown in
  full, per file, alongside the jobs that were left alone and why -- those are
  the part a reviewer of the pull request will have to act on, and burying them
  would make this screen a rubber stamp.

  The permission check sits at the top rather than the bottom: an App that
  cannot open a pull request should stop the operator here, not halfway through
  a batch with three of eight repositories done.
-->
<script lang="ts">
  import { AlertTriangle, ExternalLink, FileDiff } from '@lucide/svelte';
  import type { MigrationPlan, MigrationRepo } from '$lib/api/types';
  import DiffView from './DiffView.svelte';

  interface Props {
    plan: MigrationPlan | null;
    repos: readonly MigrationRepo[];
    target: string;
    busy?: boolean;
    onrescan?: () => void;
  }

  let { plan, repos, target }: Props = $props();

  const missing = $derived(plan?.missing_permissions ?? []);
  const totals = $derived.by(() => {
    let files = 0;
    let jobs = 0;
    let skips = 0;
    for (const repo of repos) {
      for (const wf of repo.workflows ?? []) {
        const rewrites = (wf.rewrites ?? []).length;
        if (rewrites > 0) files += 1;
        jobs += rewrites;
        skips += (wf.skips ?? []).length;
      }
    }
    return { files, jobs, skips };
  });
</script>

{#if missing.length > 0}
  <div class="blocker" role="alert">
    <p class="blocker-title">
      <AlertTriangle size={15} aria-hidden="true" />
      This App cannot open a pull request yet
    </p>
    <ul>
      {#each missing as item (item)}
        <li>{item}</li>
      {/each}
    </ul>
    {#if plan?.permission_hint}<p class="hint">{plan.permission_hint}.</p>{/if}
    {#if plan?.settings_url}
      <p>
        <a href={plan.settings_url} target="_blank" rel="noopener noreferrer">
          Open the App's permissions
          <ExternalLink size={12} aria-hidden="true" />
        </a>
        — then accept the change on the installation and come back.
      </p>
    {/if}
  </div>
{/if}

{#if repos.length === 0}
  <p class="lede">
    Nothing would change. Either no repository you picked has a job on a mapped label, or the
    mapping leaves them all where they are.
  </p>
{:else}
  <p class="lede">
    {totals.jobs}
    {totals.jobs === 1 ? 'job' : 'jobs'} across {totals.files}
    {totals.files === 1 ? 'file' : 'files'} in {repos.length}
    {repos.length === 1 ? 'repository' : 'repositories'}{target ? ` on ${target}` : ''}.
    {#if totals.skips > 0}
      {totals.skips} other {totals.skips === 1 ? 'job stays' : 'jobs stay'} on GitHub's runners; each
      one says why below.
    {/if}
  </p>

  <div class="repos">
    {#each repos as repo (repo.repo)}
      <section class="repo">
        <h3>{repo.repo}<span class="branch">→ {repo.default_branch}</span></h3>

        {#each repo.workflows ?? [] as wf (wf.path)}
          {#if (wf.rewrites ?? []).length > 0}
            <article>
              <p class="file">
                <FileDiff size={13} aria-hidden="true" />
                <code>{wf.path}</code>
                <span class="count"
                  >{(wf.rewrites ?? []).length}
                  {(wf.rewrites ?? []).length === 1 ? 'job' : 'jobs'}</span
                >
              </p>
              <DiffView diff={wf.diff ?? ''} />
            </article>
          {/if}
        {/each}

        {#if repo.workflows?.some((w) => (w.skips ?? []).length > 0)}
          {@const skips = (repo.workflows ?? []).flatMap((w) =>
            (w.skips ?? []).map((s) => ({ ...s, path: w.path })),
          )}
          <details>
            <summary>{skips.length} left alone in this repository</summary>
            <ul class="skips">
              {#each skips as skip, i (i)}
                <li>
                  <code>{skip.path}</code>
                  <span class="where">{skip.job || `line ${skip.line}`}</span>
                  <code class="value">{skip.value}</code>
                  <span class="reason">{skip.reason}</span>
                </li>
              {/each}
            </ul>
          </details>
        {/if}
      </section>
    {/each}
  </div>
{/if}

<style>
  .lede {
    margin: 0 0 var(--z-space-4);
    max-width: 70ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .blocker {
    margin: 0 0 var(--z-space-4);
    padding: var(--z-space-3) var(--z-space-4);
    border: 1px solid var(--z-danger-border);
    border-radius: var(--z-radius-md);
    background: var(--z-danger-subtle);
    color: var(--z-danger);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .blocker p {
    margin: 0 0 var(--z-space-2);
  }
  .blocker-title {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-medium);
  }
  .blocker ul {
    margin: 0 0 var(--z-space-2);
    padding-left: var(--z-space-5);
  }
  .blocker a {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    color: inherit;
  }
  .hint {
    opacity: 0.9;
  }
  .repos {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
  }
  .repo h3 {
    display: flex;
    align-items: baseline;
    gap: var(--z-space-2);
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-medium);
  }
  .branch {
    font-size: var(--z-text-2xs);
    font-family: var(--z-font-mono);
    color: var(--z-text-subtle);
  }
  .repo article {
    margin-bottom: var(--z-space-3);
  }
  .file {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0 0 var(--z-space-1);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .file code {
    font-family: var(--z-font-mono);
    color: var(--z-text);
  }
  .count {
    color: var(--z-text-subtle);
  }
  details summary {
    cursor: pointer;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .skips {
    margin: var(--z-space-2) 0 0;
    padding-left: var(--z-space-5);
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .skips code {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-2xs);
  }
  .skips .value {
    color: var(--z-text);
  }
  .where {
    color: var(--z-text-subtle);
  }
  .reason {
    display: block;
  }
</style>

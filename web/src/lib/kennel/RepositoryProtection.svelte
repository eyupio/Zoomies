<!--
  A repository's settings and required checks, on the repository's own page.

  Four checks live here: the default workflow token, the policy for fork pull
  requests, and the status checks the default branch requires. They read
  GitHub's settings and not the repository's files, so the fix for each is a
  setting on GitHub, and a finding here links to the page that setting is on.

  The findings are the same ones the CI tab lists, with the same waivers; this
  tab gathers them with the coverage of the two sources they read, which is
  where an operator learns that the App lacks the Administration permission.
-->
<script lang="ts">
  import { listInstallations } from '$lib/api/client';
  import type { KennelRepository } from '$lib/api/types';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Finding from '$lib/kennel/Finding.svelte';
  import {
    isProtectionCheck,
    PROTECTION_SOURCES,
    protectionChecksOn,
    settingsLinks,
  } from '$lib/kennel/protection';
  import { session } from '$lib/state/session.svelte';
  import { kennelCoverageStatus } from '$lib/status';

  let { repo }: { repo: KennelRepository } = $props();

  const findings = $derived(repo.findings.filter((f) => isProtectionCheck(f.code)));
  const sources = $derived(
    repo.coverage.filter((c) => (PROTECTION_SOURCES as readonly string[]).includes(c.source)),
  );
  const skipped = $derived(repo.skipped.filter((s) => isProtectionCheck(s.code)));
  const enabled = $derived(protectionChecksOn(repo.disabled));
  const readInFull = $derived(sources.length > 0 && sources.every((s) => s.state === 'ok'));

  // The address GitHub's pages are served from for this repository's installation,
  // which is not github.com on an Enterprise host. Not knowing it, or not being
  // allowed to ask, leaves the findings without a link and loses nothing else.
  let webURL = $state<string | undefined>(undefined);
  $effect(() => {
    const installation = repo.installation_id;
    webURL = undefined;
    if (findings.length === 0) return;
    const controller = new AbortController();
    void listInstallations(controller.signal)
      .then((result) => {
        if (!controller.signal.aborted)
          webURL = result.items?.find((i) => i.id === installation)?.web_url;
      })
      .catch(() => {});
    return () => controller.abort();
  });
</script>

<Panel
  title="Protection"
  description="Settings that decide what a workflow, or a stranger's pull request, can do here, and the status checks the default branch requires. Each is fixed on GitHub, not in a file."
>
  <div class="protection">
    {#if !repo.tracking.tracked}
      <p>
        This repository is not tracked. Start tracking it to have its settings and required checks
        read.
      </p>
    {:else if !enabled}
      <p>
        Repository settings checks are off. An administrator can turn on <code
          >kennel.settings_checks</code
        >
        in Settings. They need the App's <strong>Administration: read</strong> permission, which is asked
        for separately because GitHub offers no narrower one.
      </p>
      {#if session.can('admin')}<Button href="/settings/configuration">Open Settings</Button>{/if}
    {:else}
      <ul class="coverage" aria-label="What was read">
        {#each sources as source (source.source)}
          <li>
            <div class="head">
              <strong>{source.label}</strong>
              <Badge status={kennelCoverageStatus(source.state)} size="sm" />
            </div>
            <p>{source.reason}</p>
            {#if source.permission && source.state !== 'ok'}
              <p class="permission">
                Needs the App's <strong>{source.permission}</strong> permission.
              </p>
            {/if}
          </li>
        {/each}
      </ul>
      {#if skipped.length > 0}
        <ul class="skipped" aria-label="Checks that did not run">
          {#each skipped as skip (skip.code)}
            <li><code>{skip.code}</code>: {skip.reason}</li>
          {/each}
        </ul>
      {/if}
      {#if findings.length === 0}
        <p>
          {#if readInFull}
            No open findings. These checks cover settings and required checks only, and say nothing
            about a repository's files.
          {:else}
            No open findings, and this is not an all clear: some of what these checks need could not
            be read.
          {/if}
        </p>
      {/if}
      {#each findings as finding (finding.code + '\u0000' + finding.subject)}
        <Finding {finding}>
          {#snippet actions()}
            {#each settingsLinks(finding.code, webURL, repo.name) as link (link.href)}
              <Button size="sm" newTab href={link.href}>{link.label} on GitHub</Button>
            {/each}
          {/snippet}
        </Finding>
      {/each}
      {#if findings.length > 0}
        <p>Findings can be waived from the CI tab.</p>
      {/if}
    {/if}
  </div>
</Panel>

<style>
  .protection {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    align-items: flex-start;
  }
  .protection > :global(*) {
    max-width: 100%;
  }
  p {
    margin: 0;
    font-size: var(--z-text-sm);
  }
  .coverage,
  .skipped {
    display: grid;
    gap: var(--z-space-4);
    margin: 0;
    padding: 0;
    list-style: none;
    width: 100%;
  }
  .coverage {
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  }
  .coverage li {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .skipped {
    gap: var(--z-space-1);
    font-size: var(--z-text-sm);
  }
  .head {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .permission {
    color: var(--z-text-muted);
  }
</style>

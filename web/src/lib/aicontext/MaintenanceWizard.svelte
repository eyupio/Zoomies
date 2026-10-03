<script lang="ts">
  import { onMount } from 'svelte';
  import {
    getAIContextRepository,
    previewAIContextSetup,
    previewAIContextMaintenance,
    applyAIContextMaintenance,
  } from '$lib/api/client';
  import type { Body, Result } from '$lib/api/types';
  import Button from '$lib/components/Button.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Input from '$lib/components/Input.svelte';
  import Textarea from '$lib/components/Textarea.svelte';
  let { id, mode }: { id: string; mode: string } = $props();
  let repository = $state<Result<'getAIContextRepository'> | null>(null);
  let plan = $state<Result<'previewAIContextMaintenance'> | null>(null);
  let error = $state<unknown>(null);
  let busy = $state(false);
  let destination = $state('both');
  let exclusions = $state('');
  let retention = $state('3');
  let approvedRequest = $state<Body<'applyAIContextMaintenance'> | null>(null);
  const validMode = $derived(mode === 'amend' || mode === 'remove' ? mode : 'reinstall');
  onMount(async () => {
    try {
      repository = await getAIContextRepository(id);
      destination = repository.config.destination;
      exclusions = repository.config.exclude.join('\n');
      retention = String(repository.config.keep_snapshots);
      const saved = await previewAIContextSetup(id);
      if (saved.mode === validMode && saved.setup?.state === 'pending') {
        plan = saved;
        approvedRequest = { mode: validMode, revision: saved.revision, plan_hash: saved.plan_hash };
      }
    } catch (cause) {
      error = cause;
    }
  });
  async function review() {
    if (!repository) return;
    busy = true;
    error = null;
    try {
      const request: Body<'previewAIContextMaintenance'> = {
        mode: validMode,
        revision: repository.revision,
      };
      if (validMode === 'amend')
        request.config = {
          ...repository.config,
          destination: destination === 'repository' ? 'repository' : 'both',
          exclude: exclusions
            .split('\n')
            .map((s) => s.trim())
            .filter(Boolean),
          keep_snapshots: Number(retention),
        };
      plan = await previewAIContextMaintenance(id, request);
      approvedRequest = { ...request, plan_hash: plan.plan_hash };
    } catch (cause) {
      error = cause;
    } finally {
      busy = false;
    }
  }
  async function publish() {
    if (!approvedRequest) return;
    busy = true;
    error = null;
    try {
      plan = await applyAIContextMaintenance(id, approvedRequest);
    } catch (cause) {
      error = cause;
    } finally {
      busy = false;
    }
  }
</script>

<section class="maintenance">
  <h2>
    {validMode === 'remove'
      ? 'Remove AI Context'
      : validMode === 'amend'
        ? 'Amend AI Context'
        : 'Reinstall / repair AI Context'}
  </h2>
  {#if repository}<p>{repository.full_name} · {repository.config.source_branch}</p>{/if}
  <p>
    Merge or close the previous setup PR before preparing a new proposal. Every repository change is
    reviewed before publication.
  </p>
  {#if repository?.setup_pr_url}<Button href={repository.setup_pr_url} newTab size="sm"
      >Open previous PR</Button
    >{/if}
  {#if validMode === 'remove'}
    <p>
      Creating the removal PR immediately revokes Zoomies source access, clears cached snapshots,
      readers and connection consent. Merge the PR to remove the workflow, managed files, badge and
      marked instruction sections. Other text and the historical generated context branch are
      preserved. Reinstalling requires readers and connection access to be selected again.
    </p>
  {:else}
    <p>
      The README badge is automatic. Merging this PR triggers fresh generation; context stays
      unavailable until the new output is verified.
    </p>
  {/if}
  {#if validMode === 'amend' && !plan}
    <label
      >Destination<select bind:value={destination}
        ><option value="both">Repository and Zoomies</option><option value="repository"
          >Repository only</option
        ></select
      ></label
    >
    <label>Exclusions, one pattern per line<Textarea bind:value={exclusions} /></label>
    <label>Snapshots to retain<Input bind:value={retention} inputmode="numeric" /></label>
  {/if}
  {#if error}<ErrorState {error} title="Maintenance could not finish" />{/if}
  {#if plan}
    <h3>Review repository changes</h3>
    {#each plan.files as file (file.path)}
      <details>
        <summary
          >{file.delete ? 'Delete' : file.previous_sha ? 'Update' : 'Create'} {file.path}</summary
        >
        <pre>{file.delete ? 'Remove this managed file.' : file.content}</pre>
      </details>
    {/each}
    {#if plan.setup?.pr_url}<Button href={plan.setup.pr_url} newTab>Open maintenance PR</Button>
    {:else}<Button variant="primary" loading={busy} disabled={busy} onclick={publish}
        >{validMode === 'remove'
          ? 'Revoke access and create removal PR'
          : 'Create reviewed maintenance PR'}</Button
      >{/if}
    {#if !plan.setup}<Button
        disabled={busy}
        onclick={() => {
          plan = null;
          approvedRequest = null;
        }}>Change review</Button
      >{/if}
  {:else}<Button variant="primary" loading={busy} disabled={busy || !repository} onclick={review}
      >Review changes</Button
    >{/if}
  <Button href="/ai-context">Back to AI Context</Button>
</section>

<style>
  .maintenance {
    display: grid;
    gap: 1rem;
    max-width: 60rem;
  }
  label {
    display: grid;
    gap: 0.5rem;
  }
  select {
    color: var(--z-text);
    background: var(--z-surface);
    padding: 0.5rem;
  }
  pre {
    max-height: 24rem;
    overflow: auto;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  summary {
    cursor: pointer;
  }
</style>

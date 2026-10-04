<!--
  What a pool the controller keeps is, and where its numbers come from.

  The sentence at the top is the controller's own: it names the hosts that count
  and what they hold between them, so the maximum on the page is never a figure
  nobody typed and nobody can account for. The facts under it are the same
  things as a list, for the operator who is looking for one of them rather than
  reading the paragraph.
-->
<script lang="ts">
  import type { AutoPools, Pool } from '$lib/api/types';
  import { classWord } from '$lib/hosts/tags';
  import { fleet } from '$lib/state/fleet.svelte';
  import { archWords, capWords, limitWords, runnerWords, warmWords } from './auto';

  interface Props {
    pool: Pool;
    /** What the controller says about the classes, where it has been asked. */
    status?: AutoPools | null;
  }

  let { pool, status = null }: Props = $props();

  const auto = $derived(pool.auto);
  const limits = $derived(status?.classes.find((c) => c.class === auto?.class));
  // The label that asks for this pool's class by name. Read from the pool rather
  // than built here, because an arm64 pool's has the architecture in it.
  const sizeLabel = $derived(
    auto ? (pool.labels ?? []).find((label) => label.endsWith(`-${auto.class}`)) : undefined,
  );
  // The pool names its hosts; the cache knows which of them has a page.
  const hosts = $derived(
    (auto?.hosts ?? []).map((name) => ({ name, id: fleet.hosts.find((h) => h.name === name)?.id })),
  );
</script>

{#if auto}
  <div class="body" data-testid="pool-auto">
    <p class="summary">{auto.summary}</p>
    <dl class="facts">
      <dt>Kept for</dt>
      <dd>
        {archWords(auto.arch)} hosts in the {classWord(auto.class).toLowerCase()} class
        {#if limits}<span class="muted">· {limitWords(limits)}</span>{/if}
      </dd>

      <dt>Hosts</dt>
      <dd data-testid="pool-auto-hosts">
        {#if hosts.length === 0}
          <span class="muted">None count at the moment</span>
        {:else}
          {#each hosts as host, index (host.name)}
            {#if host.id}<a href="/hosts/{host.id}">{host.name}</a>{:else}{host.name}{/if}{index <
            hosts.length - 1
              ? ', '
              : ''}
          {/each}
        {/if}
      </dd>

      <dt>Maximum</dt>
      <dd class="tabular">
        {pool.max_runners ?? 0}
        <span class="muted"
          >· {auto.slots} from its hosts{auto.cap > 0 ? `, ${capWords(auto.cap)}` : ''}</span
        >
      </dd>

      <dt>Minimum</dt>
      <dd class="tabular">
        {pool.min_runners ?? 0}
        <span class="muted">· {warmWords(auto.warm)}</span>
      </dd>

      {#if limits}
        <dt>Runner</dt>
        <dd>
          {runnerWords(limits)}
          <span class="muted">· or what the host's own runner profile says</span>
        </dd>
      {/if}

      {#if sizeLabel}
        <dt>Ask for it</dt>
        <dd>
          <span class="mono">{sizeLabel}</span>
          <span class="muted"
            >· a job that writes it runs here and nowhere else. A job that writes only the base
            label is sent here on a best-effort basis.</span
          >
        </dd>
      {/if}
    </dl>
    <p class="note">
      Its maximum follows its hosts, so it is not edited. Keep runners ready, cap it, or pause it
      from its settings.
    </p>
  </div>
{/if}

<style>
  .body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  .summary,
  .note {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .facts {
    display: grid;
    grid-template-columns: minmax(0, 6.5rem) minmax(0, 1fr);
    gap: var(--z-space-2) var(--z-space-3);
    margin: 0;
    font-size: var(--z-text-xs);
  }
  dt {
    color: var(--z-text-muted);
  }
  dd {
    margin: 0;
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .muted {
    color: var(--z-text-subtle);
  }
  a {
    color: var(--z-accent);
  }
</style>

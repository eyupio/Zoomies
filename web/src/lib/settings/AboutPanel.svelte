<!--
  About this instance.

  Version, where the database is, and how many browsers are currently attached
  to the event stream -- the last one is the quiet answer to "is the live view
  actually connected?", which is worth being able to check from the server's
  side rather than the browser's.
-->
<script lang="ts">
  import { BookOpen, ExternalLink } from '@lucide/svelte';
  import { getSettings } from '$lib/api/client';
  import type { Settings } from '$lib/api/types';
  import { formatNumber, pluralise } from '$lib/format';
  import { session } from '$lib/state/session.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  const DOCS: { label: string; description: string; href: string }[] = [
    {
      label: 'README',
      description: 'What Zoomies is and how to run it.',
      href: 'https://github.com/eyupio/zoomies#readme',
    },
    {
      label: 'Configuration',
      description: 'Every setting, what it does and what it defaults to.',
      href: 'https://github.com/eyupio/zoomies/blob/main/docs/configuration.md',
    },
    {
      label: 'API surface',
      description: 'Every endpoint this UI and the CLI are built on.',
      href: 'https://github.com/eyupio/zoomies/blob/main/docs/api-surface.md',
    },
    {
      label: 'This instance’s OpenAPI document',
      description: 'The specification, served by this controller.',
      href: '/api/openapi.yaml',
    },
  ];

  let settings = $state<Settings | null>(null);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let reload = $state(0);

  $effect(() => {
    void reload;
    const controller = new AbortController();
    loading = true;
    void getSettings(controller.signal)
      .then((result) => {
        settings = result;
        error = null;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        error = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  const version = $derived(settings?.version || session.meta?.version || 'unknown');
</script>

<div class="panel">
  <header>
    <h2>About</h2>
    <p>This controller, and where to read more.</p>
  </header>

  <div class="body">
    <LoadingBoundary {loading} {error} onretry={() => (reload += 1)}>
      {#snippet skeleton()}
        <Skeleton lines={4} />
      {/snippet}

      <dl class="facts">
        <dt>Version</dt>
        <dd class="mono">{version}</dd>

        <dt>Database</dt>
        <dd>
          {#if settings?.database_path}
            <CopyButton value={settings.database_path} label="Copy the database path" showValue />
          {:else}
            <span class="muted">Not reported</span>
          {/if}
        </dd>

        <dt>Event subscribers</dt>
        <dd class="tabular">
          {formatNumber(settings?.event_subscribers ?? 0)}
          <span class="muted">
            · {pluralise(settings?.event_subscribers ?? 0, 'client')} attached to the live event stream
            right now, this browser included.
          </span>
        </dd>

        {#if session.meta?.external_url}
          <dt>External URL</dt>
          <dd class="mono">{session.meta.external_url}</dd>
        {/if}

        {#if session.meta?.webhook_url}
          <dt>Webhook URL</dt>
          <dd>
            <CopyButton value={session.meta.webhook_url} label="Copy the webhook URL" showValue />
          </dd>
        {/if}
      </dl>
    </LoadingBoundary>

    <section aria-labelledby="docs-heading">
      <h3 id="docs-heading">Documentation</h3>
      <ul class="docs">
        {#each DOCS as doc (doc.href)}
          <li>
            <a href={doc.href} target="_blank" rel="noopener noreferrer">
              <BookOpen size={14} aria-hidden="true" />
              <span>
                <span class="doc-label">{doc.label}</span>
                <span class="doc-description">{doc.description}</span>
              </span>
              <ExternalLink size={13} aria-hidden="true" class="out" />
              <span class="sr-only">Opens in a new tab</span>
            </a>
          </li>
        {/each}
      </ul>
    </section>
  </div>
</div>

<style>
  .panel {
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  header {
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  header p {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-6);
    padding: var(--z-space-5);
  }
  .facts {
    display: grid;
    grid-template-columns: minmax(0, 11rem) minmax(0, 1fr);
    gap: var(--z-space-3) var(--z-space-4);
    margin: 0;
    font-size: var(--z-text-base);
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
    font-size: var(--z-text-xs);
  }
  h3 {
    margin: 0 0 var(--z-space-3);
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .docs {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .docs a {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    padding: var(--z-space-3);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    color: var(--z-text);
    text-decoration: none;
  }
  .docs a:hover {
    background: var(--z-surface-hover);
    border-color: var(--z-border-strong);
  }
  .doc-label {
    display: block;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    color: var(--z-accent);
  }
  .doc-description {
    display: block;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .docs :global(.out) {
    margin-left: auto;
    color: var(--z-text-subtle);
  }
</style>

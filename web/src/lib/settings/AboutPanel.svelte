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
  import { API_SURFACE_URL, CONFIGURATION_URL, DOCS_URL, REPO_URL, SECURITY_URL } from '$lib/links';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  /*
    These point at the site rather than at Markdown files in the repository:
    the site renders the same files, and it is the address worth passing on to
    whoever asks what this thing is.
  */
  const DOCS: { label: string; description: string; href: string }[] = [
    {
      label: 'Documentation',
      description: 'What Zoomies is, how to run it, and how to look after it.',
      href: DOCS_URL,
    },
    {
      label: 'Configuration',
      description: 'Every setting, what it does and what it defaults to.',
      href: CONFIGURATION_URL,
    },
    {
      label: 'Security',
      description: 'What each setting costs, and what the safe defaults protect.',
      href: SECURITY_URL,
    },
    {
      label: 'API surface',
      description: 'Every endpoint this UI and the CLI are built on.',
      href: API_SURFACE_URL,
    },
    {
      label: 'Source on GitHub',
      description: 'Zoomies is AGPL-3.0 licensed. Read it before you run it.',
      href: REPO_URL,
    },
    {
      label: 'This instance’s OpenAPI document',
      description: 'The specification, served by Zoomies.',
      href: '/api/openapi.yaml',
    },
  ];

  let settings = $state<Settings | null>(null);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let reload = $state(0);

  /*
    The facts come from the settings endpoint, which needs the administrator
    role. Everybody else still gets the version, which every page already
    knows from /meta, rather than an error about a request they never asked
    for.
  */
  const canAdmin = $derived(session.can('admin'));

  $effect(() => {
    void reload;
    if (!canAdmin) {
      loading = false;
      return;
    }
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

<PageHeader
  title="About"
  subtitle="Zoomies, and where to read more."
  onrefresh={canAdmin
    ? () => {
        reload += 1;
      }
    : undefined}
/>

<div class="panel">
  <!--
    The one place in the product that is allowed to be about the product rather
    than about the fleet, so the mark gets the room the brand guide asks for
    instead of the favicon-scale paw used by the smallest UI placements. 128px
    is the guide's minimum for the circular dog and the reason this slot is
    where it belongs: it is the only identity placement in the app with the
    room for the primary mark.

    The description is here rather than only in the panel's header because the
    header says what the panel is, and somebody who has arrived at a controller
    a colleague installed is owed one line saying what the thing itself is.
  -->
  <div class="identity">
    <Logo variant="mark" size={128} label="" />
    <div>
      <p class="name">Zoomies</p>
      <p class="descriptor">Self-hosted Git runners</p>
      <p class="description">
        A lightweight fleet controller for GitHub Actions runners. Ephemeral runners by default, on
        machines you own.
      </p>
    </div>
  </div>

  <div class="body">
    <LoadingBoundary {loading} {error} onretry={() => (reload += 1)}>
      {#snippet skeleton()}
        <Skeleton lines={4} />
      {/snippet}

      <dl class="facts">
        <dt>Version</dt>
        <dd class="mono">{version}</dd>

        {#if settings}
          <dt>Database</dt>
          <dd>
            {#if settings.database_path}
              <CopyButton value={settings.database_path} label="Copy the database path" showValue />
            {:else}
              <span class="muted">Not reported</span>
            {/if}
          </dd>

          <dt>Event subscribers</dt>
          <dd class="tabular">
            {formatNumber(settings.event_subscribers ?? 0)}
            <span class="muted">
              · {pluralise(settings.event_subscribers ?? 0, 'client')} attached to the live event stream
              right now, this browser included.
            </span>
          </dd>
        {/if}

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
      <h2 id="docs-heading">Documentation</h2>
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
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    overflow: hidden;
  }
  /*
    Wraps rather than shrinks: the circular dog has a minimum size in the brand
    guide, so on a narrow screen the text goes under the mark instead of the
    mark going under its minimum.
  */
  .identity {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-4);
    padding: var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
    background: var(--z-surface-sunken);
  }
  .identity > div {
    flex: 1 1 14rem;
  }
  .name {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-bold);
    letter-spacing: var(--z-tracking-tight);
    color: var(--z-text);
  }
  .descriptor {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    letter-spacing: var(--z-tracking-wider);
    text-transform: uppercase;
    color: var(--z-text-subtle);
  }
  .description {
    max-width: 46ch;
    margin: var(--z-space-3) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
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
    grid-template-columns: var(--z-facts-columns);
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
  h2 {
    margin: 0 0 var(--z-space-3);
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
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
    border: var(--z-border-width) solid var(--z-border);
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

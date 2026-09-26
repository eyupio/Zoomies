<script lang="ts">
  /**
   * The fleet status page: the one page for somebody with no account.
   *
   * It polls GET /api/v1/status and does nothing else. In particular it never
   * opens /api/v1/events -- every frame on that stream is a resource view and
   * carries names, and this page exists on the promise that it carries none.
   * Everything it renders is a fixed word, a band, a whole number of minutes
   * or a public sentence the server looked up by code.
   */
  import { onMount } from 'svelte';
  import {
    POLL_MS,
    STATE_PRESENTATION,
    bandWords,
    sinceWords,
    waitWords,
    type FleetStatus,
  } from './present';

  type Load =
    | { kind: 'loading' }
    | { kind: 'ready'; status: FleetStatus; stale: boolean }
    | { kind: 'signin' }
    | { kind: 'off' }
    | { kind: 'failed' };

  let load = $state<Load>({ kind: 'loading' });
  let now = $state(new Date());

  async function refresh(): Promise<void> {
    try {
      const res = await fetch('/api/v1/status', {
        credentials: 'same-origin',
        headers: { Accept: 'application/json' },
      });
      now = new Date();
      if (res.status === 401) {
        load = { kind: 'signin' };
        return;
      }
      if (res.status === 404) {
        load = { kind: 'off' };
        return;
      }
      if (!res.ok) throw new Error(`status ${res.status}`);
      load = { kind: 'ready', status: (await res.json()) as FleetStatus, stale: false };
    } catch {
      // Keep what was last shown, and say it may be out of date, rather than
      // blanking a page somebody is watching for a change.
      load = load.kind === 'ready' ? { ...load, stale: true } : { kind: 'failed' };
    }
  }

  onMount(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), POLL_MS);
    return () => window.clearInterval(timer);
  });

  const presentation = $derived(
    load.kind === 'ready' ? STATE_PRESENTATION[load.status.state] : undefined,
  );
</script>

<main class="page" id="main">
  <h1 class="title">Fleet status</h1>

  {#if load.kind === 'loading'}
    <p class="note" role="status">Checking the fleet…</p>
  {:else if load.kind === 'signin'}
    <p class="note">
      This fleet’s status is shown to people signed in to it. <a href="/login">Sign in</a> to see it.
    </p>
  {:else if load.kind === 'off'}
    <p class="note">This controller does not publish a fleet status.</p>
  {:else if load.kind === 'failed'}
    <p class="note" role="alert">
      The fleet status could not be loaded. This page tries again every thirty seconds.
    </p>
  {:else if presentation}
    {@const status = load.status}
    <section class="state tone-{presentation.tone}" aria-live="polite" aria-labelledby="state-word">
      <svg class="shape" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        {#if presentation.shape === 'triangle'}
          <path d="M12 3 22 20H2Z" />
        {:else if presentation.shape === 'dashed'}
          <circle cx="12" cy="12" r="8" class="dashed" />
        {:else}
          <circle cx="12" cy="12" r="8" />
        {/if}
      </svg>
      <div>
        <p class="word" id="state-word">{status.state}</p>
        <p class="headline">{presentation.headline}</p>
        <p class="since">Since {sinceWords(status.since, now)}</p>
      </div>
    </section>
    {#if load.stale}
      <p class="note" role="status">Could not refresh; this is what the fleet said last.</p>
    {/if}

    <section aria-labelledby="numbers">
      <h2 id="numbers" class="heading">The queue</h2>
      <dl class="facts">
        <div>
          <dt>Jobs waiting</dt>
          <dd>{bandWords(status.queued)}</dd>
        </div>
        <div>
          <dt>Jobs running</dt>
          <dd>{bandWords(status.running)}</dd>
        </div>
        <div>
          <dt>Typical wait</dt>
          <dd>{waitWords(status.median_wait_minutes)}</dd>
        </div>
        <div>
          <dt>Longest waits</dt>
          <dd>{waitWords(status.p95_wait_minutes)}</dd>
        </div>
      </dl>
    </section>

    <section aria-labelledby="reasons">
      <h2 id="reasons" class="heading">What is happening</h2>
      {#if status.reasons.length === 0}
        <p class="note">Nothing is wrong with the fleet.</p>
      {:else}
        <ul class="reasons">
          {#each status.reasons as reason (reason.code)}
            <li class="reason sev-{reason.severity}">
              <span class="sev">{reason.severity === 'error' ? 'Blocking' : 'Slowing'}</span>
              <span class="sentence">{status.explanations[reason.code]}</span>
              <span class="meta">
                <code>{reason.code}</code>{#if reason.since}
                  · since {sinceWords(reason.since, now)}{/if}
              </span>
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    <p class="footer">Zoomies {status.version} · refreshed every thirty seconds</p>
  {/if}
</main>

<style>
  :global(html) {
    background: var(--z-bg);
    color: var(--z-text);
    font-family: var(--z-font-sans);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
  }
  :global(body) {
    margin: 0;
  }
  .page {
    max-width: var(--z-width-dialog-md);
    margin: 0 auto;
    padding: var(--z-space-8) var(--z-space-4);
    display: grid;
    gap: var(--z-space-6);
  }
  .title {
    margin: 0;
    font-size: var(--z-text-2xl);
    line-height: var(--z-leading-2xl);
    font-weight: var(--z-weight-bold);
  }
  .heading {
    margin: 0 0 var(--z-space-2);
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
  }
  .note,
  .footer,
  .since,
  .meta {
    color: var(--z-text-muted);
    margin: 0;
  }
  .footer {
    font-size: var(--z-text-xs);
  }
  a {
    color: var(--z-accent);
  }
  a:focus-visible {
    outline: none;
    box-shadow: var(--z-focus-ring);
    border-radius: var(--z-radius-sm);
  }
  .state {
    display: flex;
    gap: var(--z-space-4);
    align-items: flex-start;
    padding: var(--z-space-5);
    border-radius: var(--z-radius-lg);
    border: var(--z-border-width) solid;
  }
  .state p {
    margin: 0;
  }
  .tone-idle {
    background: var(--z-idle-subtle);
    border-color: var(--z-idle-border);
    color: var(--z-idle);
  }
  .tone-pending {
    background: var(--z-pending-subtle);
    border-color: var(--z-pending-border);
    color: var(--z-pending);
  }
  .tone-danger {
    background: var(--z-danger-subtle);
    border-color: var(--z-danger-border);
    color: var(--z-danger);
  }
  .shape {
    flex: none;
    width: var(--z-space-10);
    height: var(--z-space-10);
    fill: none;
    stroke: currentColor;
    stroke-width: var(--z-border-width-thick);
  }
  .tone-danger .shape {
    fill: currentColor;
  }
  .dashed {
    stroke-dasharray: 4 3;
  }
  .word {
    font-size: var(--z-text-xl);
    line-height: var(--z-leading-xl);
    font-weight: var(--z-weight-bold);
    text-transform: capitalize;
  }
  .headline {
    color: var(--z-text);
  }
  .facts {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--z-space-3);
    margin: 0;
  }
  .facts div {
    background: var(--z-surface);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    padding: var(--z-space-3);
  }
  .facts dt {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
  .facts dd {
    margin: 0;
    font-weight: var(--z-weight-semibold);
  }
  .reasons {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--z-space-2);
  }
  .reason {
    display: grid;
    gap: var(--z-space-1);
    background: var(--z-surface);
    border: var(--z-border-width) solid var(--z-border);
    border-left-width: var(--z-border-width-rail);
    border-radius: var(--z-radius-md);
    padding: var(--z-space-3);
  }
  .sev-error {
    border-left-color: var(--z-danger);
  }
  .sev-warning {
    border-left-color: var(--z-pending);
  }
  .sev {
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-semibold);
  }
  .sev-error .sev {
    color: var(--z-danger);
  }
  .sev-warning .sev {
    color: var(--z-pending);
  }
  .meta {
    font-size: var(--z-text-xs);
  }
  .meta code {
    font-family: var(--z-font-mono);
  }
  @media (max-width: 480px) {
    .facts {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>

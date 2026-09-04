<!--
  The effective configuration.

  This is what the running controller actually believes, not what the file says
  -- the two differ whenever an environment variable or a runtime change is in
  play, and the difference is exactly the thing that makes a configuration
  problem hard to find. Secrets are absent rather than starred out: the API does
  not send them at all.

  A small subset can be changed here and takes effect immediately. Which subset
  is not guessed: the API publishes the keys that need a restart, and the ones
  offered for editing are the retention, scheduler, poll interval and log level
  settings its own documentation names as safe.
-->
<script lang="ts">
  import { getSettings, updateSettings, ApiError } from '$lib/api/client';
  import type { Problem, Settings } from '$lib/api/types';
  import { severityStatus } from '$lib/status';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import SettingRow from './SettingRow.svelte';

  /**
   * The settings this controller accepts while it is running, from the API's own
   * description of PATCH /settings: retention, the scheduler tunables, the poll
   * interval and the log level. Anything else is read-only here, and the
   * restart-required list from the server trims this further.
   */
  const EDITABLE_PREFIXES = ['retention.', 'scheduler.'];
  const EDITABLE_KEYS = ['github.poll_interval', 'log.level'];

  const LOG_LEVELS = [
    { value: 'debug', label: 'debug' },
    { value: 'info', label: 'info' },
    { value: 'warn', label: 'warn' },
    { value: 'error', label: 'error' },
  ];

  /** Sections in the order an operator reads them, not alphabetical. */
  const SECTION_ORDER = [
    'server',
    'database',
    'security',
    'github',
    'agent',
    'scheduler',
    'retention',
    'log',
    'oidc',
    'metrics',
  ];

  const SECTION_BLURB: Record<string, string> = {
    server: 'How this controller listens, and the URL GitHub and the agents use to reach it.',
    database: 'Where the SQLite file lives.',
    security: 'Sessions, encryption and whether authentication is on at all.',
    github: 'How Zoomies talks to GitHub, and what it falls back to when webhooks do not arrive.',
    agent: 'The agent built into this process, if it is running one.',
    scheduler: 'How eagerly runners are created and when they are given up on.',
    retention: 'How long history is kept before it is pruned.',
    log: 'How much the controller says, and in what format.',
    oidc: 'Single sign-on.',
    metrics: 'The Prometheus endpoint.',
  };

  interface Props {
    class?: string;
  }

  let { class: className = '' }: Props = $props();

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

  /* -- the rows ----------------------------------------------------------------- */

  interface Row {
    path: string;
    value: unknown;
  }

  /** Flatten the config into dotted keys, the way they are written in the file. */
  function flatten(value: unknown, prefix: string, into: Row[]): void {
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
        flatten(child, prefix ? `${prefix}.${key}` : key, into);
      }
      return;
    }
    into.push({ path: prefix, value });
  }

  const rows = $derived.by(() => {
    const out: Row[] = [];
    flatten(settings?.config ?? {}, '', out);
    return out;
  });

  const sections = $derived.by(() => {
    const grouped: Record<string, Row[]> = {};
    for (const row of rows) {
      const section = row.path.split('.')[0] ?? 'other';
      (grouped[section] ??= []).push(row);
    }
    return Object.entries(grouped).sort(
      (a, b) =>
        (SECTION_ORDER.indexOf(a[0]) + 1 || 99) - (SECTION_ORDER.indexOf(b[0]) + 1 || 99) ||
        a[0].localeCompare(b[0]),
    );
  });

  const restartKeys = $derived(settings?.restart_required_keys ?? []);

  const findingsBySetting = $derived.by(() => {
    const map: Record<string, Problem[]> = {};
    for (const finding of settings?.findings ?? []) {
      if (!finding.setting) continue;
      (map[finding.setting] ??= []).push(finding);
    }
    return map;
  });

  /** Findings the validator did not tie to a particular setting. */
  const generalFindings = $derived(
    (settings?.findings ?? []).filter(
      (finding) => !finding.setting || !rows.some((row) => row.path === finding.setting),
    ),
  );

  function isEditable(path: string): boolean {
    if (restartKeys.includes(path)) return false;
    return EDITABLE_KEYS.includes(path) || EDITABLE_PREFIXES.some((p) => path.startsWith(p));
  }

  /* -- changing one ---------------------------------------------------------------- */

  /**
   * Send one key. The API takes dotted keys directly, and answers 422 with a
   * message written for a person -- which is returned here so the row can put it
   * under the field rather than in a toast that floats away.
   */
  async function save(path: string, raw: string): Promise<string> {
    const current = rows.find((row) => row.path === path)?.value;
    const value: unknown = typeof current === 'number' ? Number(raw) : raw;
    if (typeof current === 'number' && !Number.isFinite(value as number)) {
      return 'That is not a number.';
    }
    try {
      settings = await updateSettings({ [path]: value });
      toasts.success(`${path} changed`, 'It applies to this running controller.');
      return '';
    } catch (cause) {
      if (cause instanceof ApiError) {
        return cause.fieldErrors()[path] ?? cause.message;
      }
      return 'That change could not be made. The controller log will say why.';
    }
  }
</script>

<div class="panel {className}">
  <header>
    <div>
      <h2>Configuration</h2>
      <p>
        What this controller is running with right now. Secrets are never sent to the browser, so
        they are absent rather than hidden. A change made here applies immediately and lasts until
        the process restarts; to make one permanent, put it in the configuration file.
      </p>
    </div>
  </header>

  <LoadingBoundary {loading} {error} onretry={() => (reload += 1)}>
    {#snippet skeleton()}
      <div class="pad"><Skeleton lines={8} /></div>
    {/snippet}

    {#if settings}
      <div class="meta">
        <div>
          <span class="meta-label">Configuration file</span>
          {#if settings.config_path}
            <CopyButton
              value={settings.config_path}
              label="Copy the configuration file path"
              showValue
            />
          {:else}
            <span class="meta-value">None. Everything comes from defaults and the environment.</span
            >
          {/if}
        </div>
      </div>

      {#if generalFindings.length > 0}
        <section class="general" aria-labelledby="general-findings">
          <h3 id="general-findings">What the validator says</h3>
          <ul>
            {#each generalFindings as finding (finding.code)}
              {@const meta = severityStatus(finding.severity)}
              <li>
                <Badge status={meta} size="sm" />
                <div>
                  <p class="finding-title">{finding.title}</p>
                  {#if finding.detail}<p class="finding-detail">{finding.detail}</p>{/if}
                  {#if finding.fix}
                    <p class="finding-detail"><strong>Fix:</strong> {finding.fix}</p>
                  {/if}
                </div>
              </li>
            {/each}
          </ul>
        </section>
      {/if}

      {#each sections as [section, sectionRows] (section)}
        <section aria-labelledby="section-{section}">
          <div class="section-head">
            <h3 id="section-{section}" class="mono">{section}</h3>
            {#if SECTION_BLURB[section]}<p>{SECTION_BLURB[section]}</p>{/if}
          </div>
          {#each sectionRows as row (row.path)}
            <SettingRow
              path={row.path}
              value={row.value}
              editable={isEditable(row.path)}
              restartRequired={restartKeys.includes(row.path)}
              findings={findingsBySetting[row.path] ?? []}
              choices={row.path === 'log.level' ? LOG_LEVELS : undefined}
              onsave={save}
            />
          {/each}
        </section>
      {/each}
    {/if}
  </LoadingBoundary>
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
    max-width: 84ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .pad {
    padding: var(--z-space-5);
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-6);
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
  }
  .meta-label {
    display: block;
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-text-muted);
    margin-bottom: var(--z-space-1);
  }
  .meta-value {
    font-size: var(--z-text-sm);
    color: var(--z-text-subtle);
  }
  .general {
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
    background: var(--z-surface-sunken);
  }
  .general ul {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .general li {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
  }
  .finding-title {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .finding-detail {
    margin: var(--z-space-1) 0 0;
    max-width: 80ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .section-head {
    padding: var(--z-space-4) var(--z-space-5) var(--z-space-2);
    border-bottom: 1px solid var(--z-border);
    background: var(--z-surface-sunken);
  }
  h3 {
    margin: 0;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .section-head p {
    margin: var(--z-space-1) 0 0;
    max-width: 80ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  section:last-child :global(.row:last-child) {
    border-bottom: 0;
  }
</style>

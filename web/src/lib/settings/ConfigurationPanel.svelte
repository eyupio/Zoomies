<!--
  The configuration.

  Everything here lives in this fleet's database, so a change made here is kept
  rather than going back to a file at the next restart. Three keys are the
  exception and say so in the row: the path to the database and the two ways of
  naming the key that unseals what is in it cannot live inside the thing they
  open.

  Eighty-eight settings is too many to scroll, so the page is built to be
  searched and filtered rather than read: one toolbar with a box that matches
  keys, summaries and environment variable names and a switch between every
  setting, the ones somebody has actually changed, and the ones the controller
  is complaining about; and beside the rows an index of the sections that says
  where you are and takes you to any other. What an operator usually wants is
  one of those three things, and all of them are one action away without the
  rows moving down the page to make room.

  Secrets are absent rather than starred out: the API does not send them at all.
-->
<script lang="ts">
  import { supportHint } from '$lib/errors';
  import { FileDown, FileUp, Lock, RotateCcw, Search, TriangleAlert } from '@lucide/svelte';
  import { getSettings, settingsExportUrl, updateSettings, ApiError } from '$lib/api/client';
  import { registerSearch } from '$lib/keys';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import type { Problem, Setting, Settings } from '$lib/api/types';
  import { severityStatus } from '$lib/status';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import DropdownMenu from '$lib/components/DropdownMenu.svelte';
  import ImportSettingsDialog from './ImportSettingsDialog.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import Input from '$lib/components/Input.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import SettingRow from './SettingRow.svelte';
  import { SECTION_BLURB, SECTIONS_ELSEWHERE, displayValue, matches } from './settings';

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

  /* -- moving a configuration ----------------------------------------------- */

  /*
    An export is a navigation rather than a fetch: the point is the browser's
    own download, and the cookie goes with it. YAML is the shape zoomies.yaml
    takes, so the file can be started from; JSON wraps the same tree with when
    it was taken and where from.
  */
  const exportItems = [
    {
      id: 'yaml',
      label: 'As zoomies.yaml',
      icon: FileDown,
      onSelect: () => window.location.assign(settingsExportUrl('yaml')),
    },
    {
      id: 'json',
      label: 'As JSON, with provenance',
      icon: FileDown,
      onSelect: () => window.location.assign(settingsExportUrl('json')),
    },
  ];
  let importOpen = $state(false);

  /* -- filtering ------------------------------------------------------------ */

  type View = 'all' | 'changed' | 'attention';

  let query = $state('');
  let view = $state<View>('all');
  let searchField = $state<HTMLInputElement | null>(null);

  /**
   * The setting a link asked for, from the address bar.
   *
   * A problem that names a key used to land somebody on a list of eighty-eight
   * and leave them to find it -- nine tenths of the work done and then stopped.
   * The row is scrolled to and marked instead, and the filters are stood down
   * for it, because a link that arrives while "Changed" is selected would
   * otherwise open a page the setting is not on.
   */
  const wanted = $derived(router.param('setting'));
  let sought = $state('');
  // Plain, not $state: the effect must not track it. The effect reads
  // `settings`, which is replaced after every save, refresh and import, and the
  // address keeps its `?setting=` for the whole visit -- so without this the
  // filters were wiped and the page scrolled back to the linked row each time
  // somebody saved a different one.
  let handled = '';

  $effect(() => {
    const key = wanted;
    if (!key) {
      handled = '';
      return;
    }
    if (!settings || key === handled) return;
    handled = key;
    // Untangled from the filters first, or the row may not be rendered to
    // scroll to. Both are cleared rather than only the one that would hide it:
    // which of them does depends on the key, and a page that sometimes honours
    // a link is worse than one that always does.
    query = '';
    view = 'all';
    sought = key;
    // After the filter change has rendered. A frame is enough, and the read is
    // guarded because a key nobody recognises simply scrolls nowhere.
    requestAnimationFrame(() => {
      document.getElementById(`setting-${key}`)?.scrollIntoView({ block: 'center' });
    });
  });

  /**
   * `/` puts the cursor in the search box. The shortcut, the guard against
   * swallowing a keystroke meant for a field, and the line in the shortcuts
   * sheet all already exist in keys.ts -- a page only has to say which of its
   * inputs is the search one, as Jobs, Runners and the log viewer do.
   */
  $effect(() => registerSearch(searchField));

  /*
    Every setting this page owns. A section another page edits is left to it --
    the backup schedule belongs beside the backups it produces -- and the rail
    links there, so the keys are still findable from here without this page
    offering a second editor for them.
  */
  /*
    A controller that runs no agent has nothing for the rest of the agent
    section to describe: its backend, socket and capacity are the joined
    hosts' business, set on those hosts. Listing them here invites an edit that
    changes nothing. The switch that would bring an agent back stays, so the
    choice is still reachable from this page.
  */
  const noAgent = $derived((settings?.findings ?? []).some((f) => f.code === 'agent.none'));
  const all = $derived<readonly Setting[]>(
    (settings?.settings ?? []).filter(
      (s) =>
        !SECTIONS_ELSEWHERE[s.section] &&
        !(noAgent && s.key.startsWith('agent.') && s.key !== 'agent.embedded'),
    ),
  );
  /*
    The pointer is shown while a search is running too, and that is the point
    of it: somebody who types "backup interval" into this page's search box
    finds no rows, and the one thing that would help is being told where the
    rows went. It is hidden only when the query plainly means something else.
  */
  const elsewhere = $derived(
    Object.entries(SECTIONS_ELSEWHERE).filter(
      ([name]) =>
        (settings?.settings ?? []).some((s) => s.section === name) &&
        (query.trim() === '' ||
          (settings?.settings ?? []).some((s) => s.section === name && matches(s, query))),
    ),
  );

  const findingsBySetting = $derived.by(() => {
    const map: Record<string, Problem[]> = {};
    for (const finding of settings?.findings ?? []) {
      if (!finding.setting) continue;
      (map[finding.setting] ??= []).push(finding);
    }
    return map;
  });

  const changedCount = $derived(all.filter((s) => s.source !== 'default').length);
  const attentionCount = $derived(
    all.filter((s) => s.pending || (findingsBySetting[s.key]?.length ?? 0) > 0).length,
  );

  const views = $derived([
    { value: 'all' as const, label: `All ${all.length}` },
    { value: 'changed' as const, label: `Changed ${changedCount}` },
    ...(attentionCount > 0
      ? [{ value: 'attention' as const, label: `Attention ${attentionCount}` }]
      : []),
  ]);

  const visible = $derived(
    all.filter((s) => {
      if (!matches(s, query)) return false;
      if (view === 'changed') return s.source !== 'default';
      if (view === 'attention') return s.pending || (findingsBySetting[s.key]?.length ?? 0) > 0;
      return true;
    }),
  );

  /*
    A setting the environment is holding is not something an administrator can
    do anything about from here, so it does not belong among the ones they can.
    It is gathered at the end instead, where it answers "why is that value not
    what I set" without standing between somebody and the setting they came for.
  */
  const held = $derived(visible.filter((s) => s.source === 'environment'));
  const changeable = $derived(visible.filter((s) => s.source !== 'environment'));

  /* The API already orders the list, so grouping preserves that order. */
  const sections = $derived.by(() => {
    const out: { name: string; rows: Setting[] }[] = [];
    for (const setting of changeable) {
      const name = setting.section ?? 'other';
      const last = out[out.length - 1];
      if (last?.name === name) last.rows.push(setting);
      else out.push({ name, rows: [setting] });
    }
    return out;
  });

  /** Findings the validator did not tie to a setting this page renders. */
  const generalFindings = $derived(
    (settings?.findings ?? []).filter(
      (finding) => !finding.setting || !all.some((s) => s.key === finding.setting),
    ),
  );

  const pending = $derived(settings?.pending_restart ?? []);

  /* -- the index ------------------------------------------------------------ */

  let rows = $state<HTMLDivElement | null>(null);
  let controls = $state<HTMLDivElement | null>(null);
  let activeSection = $state('');

  /*
    Which section the reader is in, so the index can say so. The last heading
    that has passed under the toolbar wins. A scroll listener rather than an
    intersection observer, because the answer depends on where each heading is
    relative to one line and not on how much of a section is on screen -- and
    a section of one row is on screen entirely while the reader is still three
    sections above it.
  */
  $effect(() => {
    const root = rows;
    const bar = controls;
    if (!root || !bar || sections.length < 2) return;
    let frame = 0;
    const measure = () => {
      frame = 0;
      const line = bar.getBoundingClientRect().bottom;
      let current = '';
      for (const head of root.querySelectorAll<HTMLElement>('[data-section]')) {
        if (current === '' || head.getBoundingClientRect().top <= line) {
          current = head.dataset.section ?? '';
        } else {
          break;
        }
      }
      activeSection = current;
    };
    const onScroll = () => {
      if (!frame) frame = requestAnimationFrame(measure);
    };
    measure();
    addEventListener('scroll', onScroll, { passive: true });
    return () => {
      removeEventListener('scroll', onScroll);
      if (frame) cancelAnimationFrame(frame);
    };
  });

  /* -- changing one --------------------------------------------------------- */

  /**
   * Send one key. The API takes dotted keys directly, and answers 422 with a
   * message written for a person -- which is returned here so the row can put
   * it under the field rather than in a toast that floats away.
   */
  async function save(key: string, value: unknown): Promise<string> {
    try {
      const result = await updateSettings({ [key]: value } as Record<string, unknown>);
      settings = result;
      // Some of what changed is also what every page reads from /meta.
      void session.reloadMeta();
      const now = result.settings?.find((s) => s.key === key);
      if (value === null) {
        toasts.success(`${key} reset`, 'It is back to the configuration file or the default.');
      } else if (now?.pending) {
        toasts.success(`${key} saved`, 'It takes effect the next time the controller restarts.');
      } else {
        toasts.success(`${key} changed`, 'It is in force now.');
      }
      return '';
    } catch (cause) {
      if (cause instanceof ApiError) {
        const errors = cause.fieldErrors();
        if (errors[key]) return errors[key];
        /*
          A refusal about the whole request rather than about this field --
          the combination would leave a controller that will not start, and
          the setting at fault is one nobody touched. Its reason is the useful
          half, so it is carried through rather than dropped for the envelope's
          one-line summary.
        */
        const general = Object.values(errors).filter(Boolean);
        return general.length > 0 ? `${cause.message}: ${general.join(' ')}` : cause.message;
      }
      return `That change could not be made. ${supportHint()}`;
    }
  }
</script>

<PageHeader
  title="Configuration"
  subtitle="What Zoomies runs with, and where each value came from. A change made here is kept in the database and survives a restart; the environment always has the last word."
  onrefresh={() => {
    reload += 1;
  }}
>
  <DropdownMenu
    items={exportItems}
    label="Export the settings"
    triggerLabel="Export"
    triggerIcon={FileDown}
  />
  <Button variant="secondary" icon={FileUp} onclick={() => (importOpen = true)}>Import</Button>
</PageHeader>

<ImportSettingsDialog
  bind:open={importOpen}
  onapplied={(result) => {
    settings = result;
    void session.reloadMeta();
  }}
/>

<LoadingBoundary {loading} {error} onretry={() => (reload += 1)}>
  {#snippet skeleton()}
    <div class="panel pad"><Skeleton lines={8} /></div>
  {/snippet}

  {#if settings}
    {#if pending.length > 0}
      <section class="notice waiting" aria-labelledby="pending-restart">
        <Badge tone="draining" label="Saved" size="sm" dot={false} />
        <div>
          <h2 id="pending-restart">
            {pending.length === 1
              ? 'One setting is waiting for a restart'
              : `${pending.length} settings are waiting for a restart`}
          </h2>
          <p>
            Stored, and in force the next time Zoomies starts. It cannot apply them to itself —
            rebinding a listener or rebuilding the container backends under running jobs is how a
            reload becomes an outage.
          </p>
          <ul class="keys">
            {#each pending as key (key)}
              <li class="mono">{key}</li>
            {/each}
          </ul>
        </div>
      </section>
    {/if}

    {#if generalFindings.length > 0}
      <section class="general" aria-labelledby="general-findings">
        <h2 id="general-findings">
          <TriangleAlert size={14} aria-hidden="true" />
          What the validator says
        </h2>
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
                <!--
                  Which layer set it, when that is not where the fix points.
                  Told only to "change server.bind", an operator edits the
                  configuration file -- and a value stored here or pinned by
                  a variable is one the file cannot change, so they restart
                  into the same complaint having learned nothing.
                -->
                {#if finding.undo}
                  <p class="finding-detail">
                    <strong
                      >{finding.source === 'environment'
                        ? 'The environment is setting this'
                        : 'This value is stored in this fleet'}</strong
                    >
                    {finding.source === 'environment'
                      ? ', and it is the last word: neither the file nor this page can change it. With the controller stopped, run'
                      : ', so editing the configuration file will not change it. If it is stopping the controller starting, run this against the stopped controller:'}
                    <code>{finding.undo}</code>
                  </p>
                {/if}
              </div>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    <div class="panel">
      <!--
        The search and the view switch stay reachable while the list scrolls.
        On a page this long, a filter that has to be scrolled back to is one
        that gets used once.
      -->
      <div class="controls" bind:this={controls}>
        <div class="search">
          <Input
            bind:value={query}
            bind:element={searchField}
            size="sm"
            icon={Search}
            placeholder="Search settings, summaries and ZOOMIES_* names  (press /)"
            ariaLabel="Search settings"
          />
        </div>
        <Segmented
          options={views}
          value={view}
          label="Which settings to show"
          onchange={(next) => (view = next as View)}
        />
      </div>

      {#if sections.length === 0 && held.length === 0}
        <p class="empty">
          <RotateCcw size={14} aria-hidden="true" />
          Nothing matches
          {#if query}“{query}”{/if}
          {#if view === 'changed'}among the settings that have been changed{/if}
          {#if view === 'attention'}among the settings needing attention{/if}.
        </p>
      {/if}

      <div class="body">
        <div class="rows" bind:this={rows}>
          {#each elsewhere as [name, where] (name)}
            <section class="elsewhere" aria-labelledby="section-{name}">
              <div class="section-head" data-section={name}>
                <h2 id="section-{name}" class="mono">{name}</h2>
                {#if SECTION_BLURB[name]}<p>{SECTION_BLURB[name]}</p>{/if}
              </div>
              <p class="pointer">
                These are set on <a href={where.href}>{where.page}</a>, beside what they affect.
              </p>
            </section>
          {/each}
          {#each sections as section (section.name)}
            <section aria-labelledby="section-{section.name}">
              <div class="section-head" data-section={section.name}>
                <h2 id="section-{section.name}" class="mono">{section.name}</h2>
                {#if SECTION_BLURB[section.name]}<p>{SECTION_BLURB[section.name]}</p>{/if}
              </div>
              {#each section.rows as setting (setting.key)}
                <SettingRow
                  {setting}
                  findings={findingsBySetting[setting.key] ?? []}
                  sought={setting.key === sought}
                  onsave={save}
                />
              {/each}
            </section>
          {/each}

          {#if held.length > 0}
            <fieldset class="held">
              <legend>
                <Lock size={13} aria-hidden="true" />
                Held by the environment
              </legend>
              <p class="held-note">
                {held.length === 1 ? 'This setting is' : 'These settings are'} set by a
                <code>ZOOMIES_*</code> variable, and the environment is the last word — it overrides
                both the database and the configuration file. To change
                {held.length === 1 ? 'it' : 'them'}, amend the environment file Zoomies starts with
                and restart it. Removing a variable hands that setting back to this page.
              </p>
              <ul class="held-rows">
                {#each held as setting (setting.key)}
                  <!--
                    The order here is the order the grid places them in: the
                    variable and its value on one line, the setting's name and
                    its key underneath. A `display: contents` row flows in
                    document order, so the columns are assigned by where each
                    span sits rather than by the grid-column it asks for.
                  -->
                  <li>
                    <span class="held-env mono">{setting.env}</span>
                    <span class="held-value mono">{displayValue(setting)}</span>
                    <span class="held-label">{setting.label}</span>
                    <span class="held-key mono">{setting.key}</span>
                  </li>
                {/each}
              </ul>
            </fieldset>
          {/if}
        </div>

        <!--
          The index. A hundred settings under fourteen headings is a page
          nobody scrolls twice, and the search box only helps somebody who
          already knows what the setting is called -- which is the case this
          page is least needed for. The index is what the operator who is
          looking around uses: it says which section they are in, takes them
          to any other, and narrows with the filters so a section the search
          emptied is not offered as somewhere to go.
        -->
        {#if sections.length > 1}
          <nav class="index" aria-label="Jump to a section">
            <p class="index-label" aria-hidden="true">On this page</p>
            <ul>
              {#each sections as section (section.name)}
                {@const here = section.name === activeSection}
                <li>
                  <a
                    href="#section-{section.name}"
                    class="mono"
                    class:current={here}
                    aria-current={here ? 'true' : undefined}
                  >
                    <span class="index-name">{section.name}</span>
                    <span class="index-count">{section.rows.length}</span>
                  </a>
                </li>
              {/each}
            </ul>
          </nav>
        {/if}
      </div>
    </div>

    <!--
      Where the layers under this page live. A footnote rather than a block
      between the toolbar and the rows: an operator reads it once, when they
      are wondering which file to edit, and the rows are what the page is for.
    -->
    <div class="paths">
      <span>
        <span class="paths-label">Configuration file</span>
        {#if settings.config_path}
          <CopyButton
            value={settings.config_path}
            label="Copy the configuration file path"
            showValue
          />
        {:else}
          <span class="paths-none">None. Everything comes from the database and the defaults.</span>
        {/if}
      </span>
      <span>
        <span class="paths-label">Database</span>
        <CopyButton value={settings.database_path ?? ''} label="Copy the database path" showValue />
      </span>
    </div>
  {/if}
</LoadingBoundary>

<style>
  .elsewhere .pointer {
    margin: 0;
    padding: var(--z-space-3) var(--z-space-5) var(--z-space-4);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }

  .panel {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .pad {
    padding: var(--z-space-5);
  }

  .notice {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    margin-bottom: var(--z-space-4);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
  }
  .notice.waiting {
    border-color: var(--z-draining-border);
    background: var(--z-draining-subtle);
  }
  .notice h2 {
    margin: 0;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .notice p {
    margin: var(--z-space-1) 0 0;
    max-width: 80ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .keys {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1) var(--z-space-3);
    margin: var(--z-space-2) 0 0;
    padding: 0;
    list-style: none;
  }
  .keys li {
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
  }

  .general {
    margin-bottom: var(--z-space-4);
    padding: var(--z-space-4) var(--z-space-5);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .general h2 {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0 0 var(--z-space-3);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
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

  /*
    Under the top bar, which is sticky too: at `top: 0` the toolbar slid up
    behind it and the search box was there but could not be reached.
  */
  .controls {
    position: sticky;
    top: var(--z-topbar-height);
    z-index: var(--z-layer-sticky);
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md) var(--z-radius-md) 0 0;
    background: var(--z-surface);
  }
  .search {
    flex: 1 1 20rem;
    min-width: 0;
    max-width: 32rem;
  }

  .empty {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    padding: var(--z-space-6) var(--z-space-5);
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }

  .body {
    display: flex;
    align-items: flex-start;
  }
  .rows {
    flex: 1;
    min-width: 0;
  }
  .section-head {
    padding: var(--z-space-3) var(--z-space-5) var(--z-space-2);
    border-bottom: var(--z-border-width) solid var(--z-border);
    background: var(--z-surface-sunken);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    /* The index's links, and a browser's own address-bar anchor, jump to this
       heading's id -- not to .section-head that carries it -- so the margin
       clearing the sticky top bar and toolbar has to sit on the heading
       itself. Set on .section-head instead, it did nothing: the jump landed
       with the heading hidden behind both bars and the first row of the
       section in its place. */
    scroll-margin-top: calc(var(--z-topbar-height) + var(--z-space-16));
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

  .index {
    position: sticky;
    top: calc(var(--z-topbar-height) + var(--z-space-12));
    flex: none;
    width: 11rem;
    padding: var(--z-space-3) var(--z-space-2) var(--z-space-3) var(--z-space-4);
    border-left: var(--z-border-width) solid var(--z-border);
  }
  .index-label {
    margin: 0 0 var(--z-space-1);
    padding: 0 var(--z-space-2);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    font-weight: var(--z-weight-medium);
    letter-spacing: var(--z-tracking-wide);
    text-transform: uppercase;
    color: var(--z-text-subtle);
  }
  .index ul {
    display: flex;
    flex-direction: column;
    gap: var(--z-nudge-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .index a {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-2);
    height: var(--z-space-6);
    padding: 0 var(--z-space-2);
    border-radius: var(--z-radius-sm);
    font-size: var(--z-text-2xs);
    color: var(--z-text-muted);
    text-decoration: none;
  }
  .index a:hover {
    background: var(--z-surface-hover);
    color: var(--z-text);
  }
  .index a.current {
    background: var(--z-accent-subtle);
    color: var(--z-accent);
  }
  .index-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .index-count {
    font-family: var(--z-font-sans);
    color: var(--z-text-subtle);
    font-variant-numeric: tabular-nums;
  }
  .index a.current .index-count {
    color: inherit;
  }

  /*
    A real fieldset, because that is what this is: a group of controls with one
    explanation that applies to all of them. It is quiet and it is last -- the
    settings nobody can change from here should not be the first thing between
    an operator and the ones they can.
  */
  .held {
    margin: var(--z-space-5);
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-pending-subtle);
    min-width: 0;
  }
  .held legend {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    padding: 0 var(--z-space-2);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .held-note {
    margin: 0 0 var(--z-space-3);
    max-width: 80ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .held-note code {
    font-size: var(--z-text-2xs);
  }
  .held-rows {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .held-rows li {
    display: grid;
    grid-template-columns: minmax(0, auto) minmax(0, 1fr);
    gap: 0 var(--z-space-3);
    align-items: baseline;
    padding-top: var(--z-space-3);
    border-top: var(--z-border-width) solid var(--z-pending-border);
  }
  .held-rows li:first-child {
    padding-top: 0;
    border-top: 0;
  }
  .held-env {
    grid-column: 1;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .held-value {
    grid-column: 2;
    font-size: var(--z-text-xs);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .held-label {
    grid-column: 1;
    font-size: var(--z-text-2xs);
    color: var(--z-text-muted);
  }
  .held-key {
    grid-column: 2;
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
    overflow-wrap: anywhere;
  }

  .paths {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1) var(--z-space-5);
    margin: var(--z-space-3) var(--z-space-1) 0;
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-subtle);
  }
  /*
    min-width: 0, so a long path truncates instead of widening the page. A
    flex item's floor is its content, so CopyButton's own ellipsis never fires
    until something above it says the item may be narrower than what is inside
    it -- and a database under a deep state directory took a phone sideways.
  */
  .paths > span {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    min-width: 0;
    max-width: 100%;
  }
  .paths-label {
    flex: none;
    color: var(--z-text-muted);
  }
  .paths-none {
    color: var(--z-text-subtle);
  }

  /*
    A tablet has no column to spare for the index, so it becomes a row of
    chips above the rows -- the same list, read across -- and the rows take
    the whole width.
  */
  @media (max-width: 1180px) {
    .body {
      flex-direction: column;
      align-items: stretch;
    }
    .index {
      order: -1;
      position: static;
      width: auto;
      padding: var(--z-space-3) var(--z-space-5);
      border-left: 0;
      border-bottom: var(--z-border-width) solid var(--z-border);
      background: var(--z-surface-sunken);
    }
    .index-label {
      display: none;
    }
    .index ul {
      flex-direction: row;
      flex-wrap: wrap;
      gap: var(--z-space-2);
    }
    .index a {
      border: var(--z-border-width) solid var(--z-border);
      background: var(--z-surface);
    }
    .index a.current {
      border-color: var(--z-accent-border);
    }
  }
  @media (max-width: 768px) {
    .held-rows li {
      grid-template-columns: minmax(0, 1fr);
    }
    .held-env,
    .held-value,
    .held-label,
    .held-key {
      grid-column: 1;
    }
  }
</style>

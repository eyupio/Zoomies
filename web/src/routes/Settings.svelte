<!--
  Settings: a section rather than a page. Your account and this browser's
  preferences, who else can sign in, and what Zoomies runs with.

  Every page has an address of its own -- /settings/users -- so a page is a
  link and a bookmark, and the browser's back button means something. Where
  there is room, the section's own rail lists the pages beside the one being
  read; on a phone `/settings` is the list and each page stands alone with a
  way back.

  Most of the pages need the administrator role, and rather than being hidden
  from everybody else they are listed with a lock and open to a sentence saying
  why: a viewer who cannot find the users page should learn why, not conclude
  the product does not have one.

  The old `?tab=` addresses are kept as redirects. Bookmarks and the links in
  problem entries were written against them.
-->
<script lang="ts">
  import { ChevronLeft, Lock } from '@lucide/svelte';
  import { href, router } from '$lib/router';
  import { roleLabel } from '$lib/roles';
  import { session } from '$lib/state/session.svelte';
  import { viewport } from '$lib/state/viewport.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import AboutPanel from '$lib/settings/AboutPanel.svelte';
  import AccountPanel from '$lib/settings/AccountPanel.svelte';
  import AppearancePanel from '$lib/settings/AppearancePanel.svelte';
  import AssistantPanel from '$lib/settings/AssistantPanel.svelte';
  import BackupsPanel from '$lib/settings/BackupsPanel.svelte';
  import ConfigurationPanel from '$lib/settings/ConfigurationPanel.svelte';
  import EventsPanel from '$lib/settings/EventsPanel.svelte';
  import McpClientsPanel from '$lib/settings/McpClientsPanel.svelte';
  import McpConnectionsPanel from '$lib/settings/McpConnectionsPanel.svelte';
  import SettingsIndex from '$lib/settings/SettingsIndex.svelte';
  import SettingsRail from '$lib/settings/SettingsRail.svelte';
  import TokensPanel from '$lib/settings/TokensPanel.svelte';
  import UpdatesPanel from '$lib/settings/UpdatesPanel.svelte';
  import UsersPanel from '$lib/settings/UsersPanel.svelte';
  import { DEFAULT_SETTINGS_PAGE, settingsPage, settingsPath } from '$lib/settings/pages';

  const phone = $derived(viewport.phone);

  /** The page the address names; empty at `/settings` itself. */
  const wanted = $derived(router.params.page ?? '');
  const page = $derived(wanted ? settingsPage(wanted) : undefined);
  const locked = $derived(page !== undefined && !session.can(page.needs));
  const legacyTab = $derived(router.param('tab'));

  // `/settings?tab=configuration&setting=x` was the address before every page
  // had its own. The tab ids became the page ids, so the redirect is a move.
  $effect(() => {
    if (!legacyTab) return;
    router.navigate(href(settingsPath(legacyTab), { setting: router.param('setting') || null }), {
      replace: true,
    });
  });

  // `/settings` alone is the list on a phone, and the first page everywhere
  // else: with the rail beside it, a landing page that only repeats the rail
  // would be a click between the operator and what they came for.
  $effect(() => {
    if (wanted || phone || legacyTab) return;
    router.navigate(settingsPath(DEFAULT_SETTINGS_PAGE), { replace: true });
  });

  // The top bar and the browser tab name the page, with the section as the
  // crumb before it.
  $effect(() => {
    router.setTitle(page ? page.label : 'Settings');
  });
</script>

{#if !wanted}
  {#if phone}
    <SettingsIndex />
  {/if}
{:else}
  <div class="section">
    {#if !phone}
      <SettingsRail current={wanted} />
    {/if}

    <div class="page">
      {#if phone}
        <a class="back" href="/settings">
          <ChevronLeft size={14} aria-hidden="true" />
          Settings
        </a>
      {/if}

      {#if !page}
        <PageHeader title="Settings" />
        <EmptyState
          title="There is no settings page called “{wanted}”"
          description="The pages are your account, MCP connections, appearance, events, users, API tokens, MCP clients, configuration, backups, updates and about."
        >
          <Button href={settingsPath(DEFAULT_SETTINGS_PAGE)}>Go to your account</Button>
        </EmptyState>
      {:else if locked}
        <PageHeader title={page.label} subtitle={page.description} />
        <EmptyState
          icon={Lock}
          title="{page.label} needs the {roleLabel(page.needs).toLowerCase()} role"
          description="You are signed in as {session.displayName} with the {roleLabel(
            session.role,
          )} role. {page.needs === 'platform'
            ? 'This page belongs to whoever runs this controller rather than to the fleet, so ask them.'
            : 'An administrator can change that on the Users page.'}"
        />
      {:else if page.id === 'account'}
        <AccountPanel />
      {:else if page.id === 'connections'}
        <McpConnectionsPanel />
      {:else if page.id === 'appearance'}
        <AppearancePanel />
      {:else if page.id === 'events'}
        <EventsPanel />
      {:else if page.id === 'users'}
        <UsersPanel />
      {:else if page.id === 'tokens'}
        <TokensPanel />
      {:else if page.id === 'mcp-clients'}
        <McpClientsPanel />
      {:else if page.id === 'configuration'}
        <ConfigurationPanel />
      {:else if page.id === 'backups'}
        <BackupsPanel />
      {:else if page.id === 'updates'}
        <UpdatesPanel />
      {:else if page.id === 'assistant'}
        <AssistantPanel />
      {:else}
        <AboutPanel />
      {/if}
    </div>
  </div>
{/if}

<style>
  .section {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-8);
  }
  .page {
    flex: 1;
    min-width: 0;
    max-width: var(--z-settings-page-max);
  }
  .back {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    margin-bottom: var(--z-space-3);
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
    text-decoration: none;
  }
  .back:hover {
    color: var(--z-accent);
  }
  /* The rail becomes a strip above the page: see SettingsRail.svelte. */
  @media (max-width: 1180px) {
    .section {
      flex-direction: column;
      align-items: stretch;
      gap: var(--z-space-4);
    }
    .page {
      max-width: none;
    }
  }
</style>

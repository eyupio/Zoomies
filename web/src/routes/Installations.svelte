<!--
  Installations: the GitHub App connections everything else depends on.

  A pool cannot exist without one, and a runner cannot register without one, so
  when this page is unhappy the rest of the product is about to be. It keeps
  itself current from `installation.updated` events and reads each App's
  remaining API quota once the list lands, because "GitHub is refusing us" and
  "we have spent our hourly quota" look identical from the Overview.
-->
<script lang="ts">
  import { supportHint } from '$lib/errors';
  import MetricGrid from '$lib/components/MetricGrid.svelte';

  import { Plug, Plus } from '@lucide/svelte';
  import {
    deleteInstallation,
    getRateLimit,
    listInstallations,
    verifyInstallation,
  } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { Installation, InstallationHealth } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import ConnectDialog from '$lib/installations/ConnectDialog.svelte';
  import InstallationCard from '$lib/installations/InstallationCard.svelte';
  import type { RateLimit } from '$lib/installations/InstallationCard.svelte';
  import ReplaceKeyDialog from '$lib/installations/ReplaceKeyDialog.svelte';
  import VerifyDialog from '$lib/installations/VerifyDialog.svelte';
  import WebhookHealth from '$lib/installations/WebhookHealth.svelte';

  const canOperate = $derived(session.can('operator'));
  const canAdmin = $derived(session.can('admin'));

  let installations = $state<Installation[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let reload = $state(0);
  let rates = $state<Record<string, RateLimit>>({});

  // First load and refetch are different things. `reload` bumps on every
  // `installation.updated` event, after every verify and after every delete, so
  // swapping the cards for skeletons each time made the one page an operator
  // watches during their first job blink its content away -- which reads as the
  // page being broken. The last known truth is better than a blank panel.
  let loadedOnce = false;

  /**
   * The list, and each App's quota behind it. A function rather than an inline
   * effect body so the refresh button can await exactly the work the page does
   * on arrival, quota reads included -- those are one request per installation
   * and are the slow half.
   */
  async function load(signal: AbortSignal): Promise<void> {
    loading = !loadedOnce;
    try {
      const result = await listInstallations(signal);
      installations = result.items ?? [];
      loadedOnce = true;
      error = null;
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === 'AbortError') return;
      error = cause;
      return;
    } finally {
      if (!signal.aborted) loading = false;
    }
    // Deliberately after the cards are on screen: a quota read is one request
    // per installation and must not hold the list behind a skeleton.
    await readRateLimits(installations, signal);
  }

  $effect(() => {
    void reload;
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  });

  // An installation going unhealthy is news; the list reloads rather than
  // patching one row, because pool counts move with it.
  $effect(() =>
    events.subscribe(['installation.updated', 'installation.deleted'], () => (reload += 1)),
  );

  /**
   * Read each App's remaining quota.
   *
   * One request per installation, and a failure is not reported: it means this
   * App cannot talk to GitHub, which the card already says in a better way.
   */
  async function readRateLimits(rows: Installation[], signal: AbortSignal): Promise<void> {
    const results = await Promise.allSettled(
      rows.map(async (row) => ({
        id: row.id ?? '',
        rate: await getRateLimit(row.id ?? '', signal),
      })),
    );
    if (signal.aborted) return;
    const next: Record<string, RateLimit> = {};
    for (const result of results) {
      if (result.status === 'fulfilled' && result.value.id)
        next[result.value.id] = result.value.rate;
    }
    rates = next;
  }

  /* -- connect ---------------------------------------------------------------- */

  let connectOpen = $state(false);

  // GitHub can send the operator back here with the manifest code in the URL,
  // and again with the installation ID once the App has been installed. Pick
  // them up, open the flow at the right step, and take them out of the address
  // bar so a reload does not try to use the code twice.
  //
  // The state matters as much as the code: it is what ties the exchange back to
  // the manifest Zoomies built, and the tab GitHub returns to is a
  // fresh one that knows nothing else about the handshake.
  const returnedCode = $derived(router.param('code'));
  const returnedState = $derived(router.param('state'));
  const returnedInstallationId = $derived(router.param('installation_id'));
  $effect(() => {
    if (!returnedCode && !returnedInstallationId) return;
    connectOpen = true;
  });

  function clearReturnedParams(): void {
    if (!returnedCode && !returnedState && !returnedInstallationId) return;
    router.setQuery({ code: null, state: null, installation_id: null, setup_action: null });
  }

  /* -- verify ------------------------------------------------------------------ */

  let verifyOpen = $state(false);
  let verifying = $state<string | null>(null);
  let verifyTarget = $state<Installation | null>(null);
  let health = $state<InstallationHealth | null>(null);
  let verifyError = $state('');

  async function verify(installation: Installation): Promise<void> {
    if (!installation.id) return;
    verifyTarget = installation;
    health = null;
    verifyError = '';
    verifying = installation.id;
    verifyOpen = true;
    try {
      health = await verifyInstallation(installation.id);
    } catch (cause) {
      verifyError =
        cause instanceof Error ? cause.message : `The check could not be made. ${supportHint()}`;
    } finally {
      verifying = null;
      reload += 1;
    }
  }

  /* -- replace the key ----------------------------------------------------------- */

  let replaceOpen = $state(false);
  let replaceTarget = $state<Installation | null>(null);

  /* -- delete -------------------------------------------------------------------- */

  let deleteOpen = $state(false);
  let deleteTarget = $state<Installation | null>(null);

  function askDelete(installation: Installation): void {
    deleteTarget = installation;
    deleteOpen = true;
  }

  async function remove(): Promise<boolean> {
    const target = deleteTarget;
    if (!target?.id) return false;
    try {
      const result = await deleteInstallation(target.id);
      toasts.success(
        `${target.target || 'Installation'} disconnected`,
        `${pluralise(result.pools_deleted ?? 0, 'pool')} deleted and ${pluralise(
          result.runners_affected ?? 0,
          'runner',
        )} affected.`,
      );
      reload += 1;
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That installation was not disconnected');
      return false;
    }
  }

  const unhealthy = $derived(installations.filter((i) => i.healthy === false).length);
</script>

<PageHeader
  title="Installations"
  subtitle="The GitHub App connections Zoomies uses to create runners and read queued jobs."
  onrefresh={() => load(new AbortController().signal)}
>
  {#snippet meta()}
    {#if installations.length > 0}
      <p class="summary" class:bad={unhealthy > 0}>
        {pluralise(installations.length, 'connection')}
        {#if unhealthy > 0}· {unhealthy} not working{/if}
      </p>
    {/if}
  {/snippet}
  {#if canAdmin}
    <Button variant="primary" icon={Plus} onclick={() => (connectOpen = true)}>
      Connect GitHub
    </Button>
  {/if}
</PageHeader>

<div class="content">
  <LoadingBoundary
    {loading}
    {error}
    empty={!loading && !error && installations.length === 0}
    onretry={() => (reload += 1)}
  >
    {#snippet skeleton()}
      <div class="list">
        {#each [0, 1] as card (card)}
          <div class="card-skeleton">
            <Skeleton width="40%" height="1.25rem" />
            <Skeleton lines={3} />
          </div>
        {/each}
      </div>
    {/snippet}

    {#snippet emptyState()}
      <EmptyState
        icon={Plug}
        title="No GitHub connection yet"
        description="Zoomies talks to GitHub as a GitHub App: that is how it sees queued jobs and registers runners. Nothing else here works until one is connected."
      >
        {#if canAdmin}
          <Button variant="primary" icon={Plus} onclick={() => (connectOpen = true)}>
            Connect GitHub
          </Button>
        {:else}
          <p class="need-admin">An administrator can connect one.</p>
        {/if}
      </EmptyState>
    {/snippet}

    <!--
      Here rather than in the skeleton: tiles computed from a list that has not
      arrived read zero, and the quota tile is fed by readings taken after it.
    -->
    <MetricGrid
      items={[
        {
          label: 'GitHub connections',
          value: String(installations.length),
          detail: 'Installed GitHub Apps',
        },
        {
          label: 'Healthy connections',
          value: String(installations.filter((i) => i.healthy === true).length),
          detail: `${installations.filter((i) => i.healthy === false).length} failing · ${installations.filter((i) => i.healthy === undefined).length} not checked`,
        },
        {
          label: 'Connected pools',
          value: String(installations.reduce((n, i) => n + (i.pool_count ?? 0), 0)),
          detail: 'Pools depending on these connections',
          href: '/pools',
        },
        {
          label: 'Low API quota',
          value: String(
            Object.values(rates).filter(
              (r) => r.limit && r.remaining !== undefined && r.remaining / r.limit < 0.1,
            ).length,
          ),
          detail: `Below 10% remaining · ${Object.keys(rates).length} quota readings`,
          tone: Object.values(rates).some(
            (r) => r.limit && r.remaining !== undefined && r.remaining / r.limit < 0.1,
          )
            ? 'warning'
            : 'neutral',
        },
      ]}
    />

    <div class="list">
      {#each installations as installation (installation.id)}
        <InstallationCard
          {installation}
          rate={rates[installation.id ?? '']}
          verifying={verifying === installation.id}
          {canOperate}
          {canAdmin}
          onverify={(target) => void verify(target)}
          onreplacekey={(target) => {
            replaceTarget = target;
            replaceOpen = true;
          }}
          ondelete={askDelete}
        />
      {/each}
    </div>
  </LoadingBoundary>

  <WebhookHealth {canOperate} />
</div>

<ConnectDialog
  bind:open={connectOpen}
  initialCode={returnedCode}
  initialState={returnedState}
  initialInstallationId={returnedInstallationId}
  oncreated={() => (reload += 1)}
  onexchanged={() => router.setQuery({ code: null, state: null })}
  onclose={clearReturnedParams}
/>

<ReplaceKeyDialog
  bind:open={replaceOpen}
  installation={replaceTarget}
  onreplaced={(target) => {
    // Straight into a verify: the operator replaced the key to fix something,
    // and "it is stored" is not the answer they came for.
    reload += 1;
    void verify(target);
  }}
  onclose={() => (replaceTarget = null)}
/>

<VerifyDialog
  bind:open={verifyOpen}
  installation={verifyTarget}
  {health}
  loading={verifying !== null}
  error={verifyError}
  onclose={() => {
    verifyTarget = null;
    health = null;
  }}
/>

<ConfirmDialog
  bind:open={deleteOpen}
  title="Disconnect installation"
  name={deleteTarget?.target}
  description="Zoomies will stop managing runners for {deleteTarget?.target ??
    'this target'}, and the sealed App credentials are deleted with it."
  consequences={[
    `${pluralise(deleteTarget?.pool_count ?? 0, 'pool')} built on this installation will be deleted.`,
    'Their runners are removed now and deregistered from GitHub. A job running on one is interrupted; drain the pools first if that matters.',
    'The App itself stays on GitHub; uninstall it there if you want it gone.',
  ]}
  confirmLabel="Disconnect"
  requireName
  onconfirm={remove}
  oncancel={() => (deleteTarget = null)}
/>

<style>
  .content {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-6);
  }
  .summary {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .summary.bad {
    color: var(--z-danger);
  }
  .list {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  .card-skeleton {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: var(--z-space-5);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .need-admin {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
</style>

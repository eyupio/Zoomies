<!--
  Adding a host, start to finish.

  Three moments on one page that stays open while the operator is over on the
  other machine: describe the host, run one command there, watch it arrive.
  The page waits by asking after its own join token every few seconds -- the
  fleet stream deliberately carries nothing about credentials -- and fills the
  host in from the live cache the instant its first frame lands. It is the
  one place in Zoomies that polls, and the reason is that the thing being
  waited for is a credential, not fleet state.

  Every input starts filled in. The controller address is the one this
  browser reached the controller on, capacity is the agent's own choice, and
  the labels on offer are the ones pools here already select hosts by --
  because a host enrolled without them is the host that is never chosen, and
  nothing else in the product says so at the moment it is cheap to fix.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { ArrowRight, Check, Plus, RefreshCw } from '@lucide/svelte';
  import {
    ApiError,
    createJoinToken,
    deleteJoinToken,
    getHost,
    getJoinToken,
  } from '$lib/api/client';
  import type { JoinToken, Pool } from '$lib/api/types';
  import { joinWords, pluralise } from '$lib/format';
  import { router } from '$lib/router';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import { hostStatus, joinTokenStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Select from '$lib/components/Select.svelte';
  import BackendList from './BackendList.svelte';
  import LabelMapEditor from './LabelMapEditor.svelte';

  type Phase = 'describe' | 'run' | 'joined';
  type Minted = JoinToken & { token?: string; command?: string };

  const STEPS = [
    { id: 'describe', title: 'Describe the host' },
    { id: 'run', title: 'Run one command on it' },
    { id: 'joined', title: 'It joins' },
  ] as const;

  const TTLS = [
    { value: '15m', label: '15 minutes' },
    { value: '1h', label: '1 hour' },
    { value: '24h', label: '24 hours' },
  ];

  /** How often the page asks whether the token has been used. */
  const POLL_MS = 3000;

  let phase = $state<Phase>('describe');
  const step = $derived(STEPS.findIndex((s) => s.id === phase));

  /* -- describe -------------------------------------------------------------- */

  /**
   * Whether an address only the controller's own machine can reach.
   *
   * The same rule as ExternalURLIsLocal in internal/config: localhost, any
   * name under .localhost, and the loopback ranges. A command carrying one of
   * these tells the new machine to join itself.
   */
  function isLocalAddress(raw: string): boolean {
    try {
      const host = new URL(raw).hostname.replace(/^\[|\]$/g, '');
      return (
        host === 'localhost' ||
        host.endsWith('.localhost') ||
        host.startsWith('127.') ||
        host === '::1'
      );
    } catch {
      return false;
    }
  }

  /**
   * Where the new host should be told to find this controller.
   *
   * server.external_url is right when it is set and is not loopback; the
   * default single-VM install sets it to http://localhost:8080, which no
   * other machine answers on. Failing that, the address this browser is using
   * is the best guess there is: a machine on the same network usually reaches
   * the controller the same way an operator does.
   */
  function suggestedControllerURL(): string {
    const configured = (session.meta?.external_url ?? '').replace(/\/+$/, '');
    if (configured && !isLocalAddress(configured)) return configured;
    return location.origin;
  }

  let controllerURL = $state(suggestedControllerURL());
  let capacity = $state('');
  let ttl = $state('15m');
  let rows = $state<{ key: string; value: string }[]>([]);
  let errors = $state<Record<string, string>>({});
  let minting = $state(false);

  const controllerError = $derived.by(() => {
    const raw = controllerURL.trim();
    if (!raw) return 'Give the address the new host will reach this controller on.';
    try {
      const u = new URL(raw);
      if (u.protocol !== 'http:' && u.protocol !== 'https:') {
        return 'Start it with http:// or https://.';
      }
      if (!u.hostname) return 'It needs a host name or IP address.';
    } catch {
      return 'Write it like https://zoomies.example.com.';
    }
    return '';
  });
  const controllerLocal = $derived(!controllerError && isLocalAddress(controllerURL));

  const capacityNumber = $derived(capacity.trim() === '' ? 0 : Number(capacity));
  const capacityError = $derived(
    capacity.trim() !== '' && (!Number.isInteger(capacityNumber) || capacityNumber < 0)
      ? 'Give a whole number of runners, or leave it blank to let the agent decide.'
      : '',
  );

  interface Suggestion {
    id: string;
    key: string;
    value: string;
    pools: string[];
  }

  /**
   * The labels pools here select hosts by, as one-click offers.
   *
   * A pool with a host_selector places runners only on hosts carrying exactly
   * those labels, so a machine enrolled without them is quietly never chosen,
   * and the first sign is a job that waits for ever. Offering the selectors
   * here shows the vocabulary the fleet already uses at the one moment it
   * costs a click to match it. Anything already in the editor is left out.
   */
  const suggestions = $derived.by(() => {
    const seen: Record<string, Suggestion> = {};
    for (const pool of fleet.pools) {
      for (const [key, value] of Object.entries(pool.host_selector ?? {})) {
        const id = `${key}=${value}`;
        const entry = seen[id] ?? { id, key, value, pools: [] };
        entry.pools.push(pool.name ?? pool.id ?? '');
        seen[id] = entry;
      }
    }
    const present = rows.map((row) => `${row.key.trim()}=${row.value.trim()}`);
    return Object.values(seen)
      .filter((s) => !present.includes(s.id))
      .sort((a, b) => a.id.localeCompare(b.id));
  });

  /** Take a suggestion: a host carries one value per key, so a key already there is updated. */
  function suggest(s: Suggestion): void {
    if (rows.some((row) => row.key.trim() === s.key)) {
      rows = rows.map((row) => (row.key.trim() === s.key ? { key: s.key, value: s.value } : row));
      return;
    }
    const blank = rows.findIndex((row) => row.key.trim() === '' && row.value.trim() === '');
    if (blank >= 0) {
      rows = rows.map((row, i) => (i === blank ? { key: s.key, value: s.value } : row));
      return;
    }
    rows = [...rows, { key: s.key, value: s.value }];
  }

  /* -- run ----------------------------------------------------------------- */

  let minted = $state<Minted | null>(null);
  /** The latest word from the controller on the token, refreshed while waiting. */
  let watched = $state<JoinToken | null>(null);
  let checkFailed = $state(false);
  let discarding = $state(false);
  let inFlight = false;

  const expired = $derived(Boolean(watched && !watched.used_at && watched.usable === false));
  const chosenURL = $derived(controllerURL.trim().replace(/\/+$/, ''));
  const installCommand = $derived(minted?.command ?? '');
  /** For a machine that already has the binary: the same join, without the download. */
  const joinCommand = $derived(
    minted?.token ? `zoomies agent join ${chosenURL} --token ${minted.token}` : '',
  );

  async function mint(): Promise<void> {
    if (controllerError || capacityError || minting) return;
    minting = true;
    errors = {};
    const labels: Record<string, string> = {};
    for (const row of rows) {
      const key = row.key.trim();
      if (key) labels[key] = row.value.trim();
    }
    try {
      minted = await createJoinToken({
        ttl,
        capacity: capacityNumber,
        labels,
        controller_url: chosenURL,
      });
      watched = null;
      checkFailed = false;
      joinedHostId = null;
      phase = 'run';
    } catch (cause) {
      if (cause instanceof ApiError) errors = cause.fieldErrors();
      toasts.fromError(cause, 'That join token was not created');
      // A refused address is a describe-step problem, wherever it was asked from.
      if (errors.controller_url) phase = 'describe';
    } finally {
      minting = false;
    }
  }

  async function check(): Promise<void> {
    const id = minted?.id;
    if (!id || inFlight) return;
    inFlight = true;
    try {
      const token = await getJoinToken(id);
      // The page may have moved on while the request was out.
      if (minted?.id !== id) return;
      watched = token;
      checkFailed = false;
      if (token.used_by_id) await arrived(token.used_by_id);
    } catch {
      // The controller may be restarting, or the network blinked. Say so
      // below and keep asking: giving up would leave a joined host unseen.
      checkFailed = true;
    } finally {
      inFlight = false;
    }
  }

  // The poll, for as long as there is something to wait for.
  $effect(() => {
    if (phase !== 'run' || expired) return;
    const timer = setInterval(() => void untrack(check), POLL_MS);
    return () => clearInterval(timer);
  });

  // The stream is faster than the poll: a host's first frame lands the moment
  // it joins, so any change in the fleet is the cue to ask now rather than in
  // three seconds. Entering the waiting state asks straight away for the same
  // reason.
  $effect(() => {
    if (phase !== 'run' || expired) return;
    void fleet.version;
    void untrack(check);
  });

  /**
   * Revoke the token and go back to the form. The settings are kept, so a
   * wrong address costs one click rather than a form.
   */
  async function discard(): Promise<void> {
    const id = minted?.id;
    if (id && !watched?.used_at) {
      discarding = true;
      try {
        await deleteJoinToken(id);
      } catch (cause) {
        // Gone already -- spent or expired under us -- is the outcome wanted.
        if (!(cause instanceof ApiError && cause.isNotFound)) {
          toasts.fromError(cause, 'That join token was not revoked');
          discarding = false;
          return;
        }
      }
      discarding = false;
    }
    reset();
  }

  function reset(): void {
    minted = null;
    watched = null;
    joinedHostId = null;
    checkFailed = false;
    errors = {};
    phase = 'describe';
  }

  /* -- joined ---------------------------------------------------------------- */

  let joinedHostId = $state<string | null>(null);
  const joinedHost = $derived(fleet.host(joinedHostId ?? undefined));

  async function arrived(hostId: string): Promise<void> {
    joinedHostId = hostId;
    phase = 'joined';
    if (fleet.host(hostId)) return;
    // The host's first frame is usually in the cache before the poll notices
    // the token was spent; when the stream is behind, ask once rather than
    // show an empty card until it catches up.
    try {
      fleet.ingestHosts([await getHost(hostId)]);
    } catch {
      // The cache catches up on its own; the card says so meanwhile.
    }
  }

  /**
   * Mirrors internal/scheduler's placement check: a pool places runners on a
   * host that offers its backend and carries every label its selector names.
   * Enabled pools only, since a disabled one creates nothing anywhere.
   */
  const placeable = $derived.by((): Pool[] => {
    const host = joinedHost;
    if (!host) return [];
    const labels = host.labels ?? {};
    const backends = host.backends ?? [];
    return fleet.pools
      .filter(
        (pool) =>
          pool.enabled !== false &&
          backends.includes(String(pool.backend ?? '')) &&
          Object.entries(pool.host_selector ?? {}).every(([k, v]) => labels[k] === v),
      )
      .sort((a, b) => (a.name ?? '').localeCompare(b.name ?? ''));
  });

  const joinedLabels = $derived(Object.entries(joinedHost?.labels ?? {}));
  const joinedPlatform = $derived([joinedHost?.os, joinedHost?.arch].filter(Boolean).join('/'));

  /* -- focus ----------------------------------------------------------------- */

  // Each phase is a new panel, and a keyboard or screen-reader user should land
  // on its heading rather than be left where the previous panel's button was.
  // The first panel is not focused: the router has already put focus on the h1.
  let panelHeading = $state<HTMLHeadingElement | null>(null);
  let announced: Phase = 'describe';
  $effect(() => {
    if (phase === announced) return;
    announced = phase;
    panelHeading?.focus();
  });
</script>

<div class="flow">
  <ol class="steps" aria-label="Progress">
    {#each STEPS as s, i (s.id)}
      <li class:done={i < step} class:active={i === step}>
        <span class="marker" aria-hidden="true">
          {#if i < step}<Check size={12} />{:else}{i + 1}{/if}
        </span>
        <span class="step-title" aria-current={i === step ? 'step' : undefined}>{s.title}</span>
      </li>
    {/each}
  </ol>

  {#if phase === 'describe'}
    <section class="panel" aria-labelledby="describe-heading">
      <h2 id="describe-heading" tabindex="-1" bind:this={panelHeading}>Describe the host</h2>
      <p class="lede">
        Everything here is already filled in. Change what is wrong for this machine, then get the
        command to run on it.
      </p>

      <form
        id="add-host-form"
        class="form"
        onsubmit={(event) => {
          event.preventDefault();
          void mint();
        }}
      >
        <Field
          label="Controller address"
          hint="What the new host will dial to reach this controller. It only ever connects outbound, so this is the one address that has to be right."
          notice={controllerLocal
            ? 'Only this machine answers on a loopback address. Use one the new host can reach, or set server.external_url so every operator gets it.'
            : undefined}
          error={errors.controller_url ?? controllerError}
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input
              bind:value={controllerURL}
              {id}
              {describedBy}
              {invalid}
              type="url"
              mono
              autocomplete="off"
              spellcheck={false}
            />
          {/snippet}
        </Field>

        <Field
          label="Capacity"
          hint="How many runners this host may run at once. Leave it blank and the agent decides from the host’s CPU count, taking half."
          error={errors.capacity ?? capacityError}
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input
              bind:value={capacity}
              {id}
              {describedBy}
              {invalid}
              type="text"
              inputmode="numeric"
              placeholder="Automatic"
              mono
            />
          {/snippet}
        </Field>

        <Field
          label="Labels"
          hint="Given to the host when it enrols. Pools with a host selector place runners only on hosts carrying those labels."
          error={errors.labels}
        >
          {#snippet children({ describedBy })}
            {#if suggestions.length > 0}
              <div class="suggest" role="group" aria-label="Labels pools here select hosts by">
                <p>Pools here select hosts by these. Add the ones that describe the new machine:</p>
                <ul>
                  {#each suggestions as s (s.id)}
                    <li>
                      <Button
                        size="sm"
                        icon={Plus}
                        title="Used by {joinWords(s.pools)}"
                        onclick={() => suggest(s)}
                      >
                        <span class="mono">{s.id}</span>
                      </Button>
                    </li>
                  {/each}
                </ul>
              </div>
            {/if}
            <LabelMapEditor bind:rows {describedBy} />
          {/snippet}
        </Field>

        <Field
          label="Token valid for"
          hint="The join token is single use, and this is how long it stays usable. Shorter is safer; the page waits with you either way."
          error={errors.ttl}
        >
          {#snippet children({ id, describedBy, invalid })}
            <Select bind:value={ttl} options={TTLS} {id} {describedBy} {invalid} />
          {/snippet}
        </Field>
      </form>

      <footer class="actions">
        <Button variant="ghost" onclick={() => router.navigate('/hosts')}>Cancel</Button>
        <Button
          variant="primary"
          type="submit"
          form="add-host-form"
          loading={minting}
          disabled={Boolean(controllerError || capacityError)}
          iconAfter={ArrowRight}
        >
          Get the command
        </Button>
      </footer>
    </section>
  {:else if phase === 'run' && minted}
    <section class="panel" aria-labelledby="run-heading">
      <h2 id="run-heading" tabindex="-1" bind:this={panelHeading}>Run this on the new host</h2>
      <p class="lede">
        One line, in a shell on that machine. It downloads the Zoomies binary and verifies it, joins
        this controller with the token, and installs the agent as a service — which is the part that
        needs root or sudo.
      </p>

      <div class="command">
        <pre class="mono"><code>{installCommand}</code></pre>
        <div class="command-actions">
          <CopyButton value={installCommand} label="Copy the install command" size="md" showLabel />
          <span class="fine">
            The token in it is shown once — only its hash is stored — and works once.
          </span>
        </div>
      </div>

      <details class="alternatives">
        <summary>The binary is already installed on that machine</summary>
        <p>Then skip the download and join directly:</p>
        <div class="command small">
          <pre class="mono"><code>{joinCommand}</code></pre>
          <div class="command-actions">
            <CopyButton value={joinCommand} label="Copy the join command" showLabel />
          </div>
        </div>
        {#if minted.token}
          <p class="token-line">
            Or the token on its own, for an answer file:
            <code class="mono">{minted.token}</code>
            <CopyButton value={minted.token} label="Copy the token" />
          </p>
        {/if}
      </details>

      <div class="watch" class:expired role="status">
        {#if expired && watched}
          <Badge status={joinTokenStatus(watched)} />
          <div class="watch-text">
            <p>
              <strong>Nobody used this token before it expired.</strong> The command above no longer works.
              Mint another with the same settings; nothing you typed is lost.
            </p>
            <Button
              variant="primary"
              icon={RefreshCw}
              loading={minting}
              onclick={() => void mint()}
            >
              Mint another token
            </Button>
          </div>
        {:else}
          <span class="pulse" aria-hidden="true"></span>
          <div class="watch-text">
            <p>
              <strong>Waiting for the host to join.</strong> This page asks the controller every few seconds
              and fills the host in the moment it arrives. Leave it open while the command runs.
            </p>
            <p class="fine">
              The token expires <RelativeTime value={minted.expires_at} plain />.
              {#if checkFailed}The last check did not reach the controller; trying again.{/if}
            </p>
          </div>
        {/if}
      </div>

      <footer class="actions">
        <Button variant="ghost" loading={discarding} onclick={() => void discard()}>
          {expired ? 'Change the settings' : 'Discard this token and change the settings'}
        </Button>
        <Button variant="secondary" href="/hosts">Check the Hosts page later</Button>
      </footer>
    </section>
  {:else if phase === 'joined'}
    <section class="panel" aria-labelledby="joined-heading">
      <h2 id="joined-heading" tabindex="-1" bind:this={panelHeading}>
        {joinedHost?.name ?? 'The host'} joined
      </h2>
      <p class="lede">
        It is enrolled, has its own credentials, and is sending heartbeats. From here on it is on
        the Hosts page like any other machine.
      </p>

      {#if joinedHost}
        <dl class="facts">
          <div>
            <dt>Status</dt>
            <dd><Badge status={hostStatus(joinedHost)} /></dd>
          </div>
          <div>
            <dt>Platform</dt>
            <dd class="mono">{joinedPlatform || '--'}</dd>
          </div>
          <div>
            <dt>Agent</dt>
            <dd>{joinedHost.version || '--'}</dd>
          </div>
          <div>
            <dt>Capacity</dt>
            <dd>{pluralise(joinedHost.capacity ?? 0, 'runner')} at once</dd>
          </div>
          <div>
            <dt>Labels</dt>
            <dd>
              {#if joinedLabels.length === 0}
                <span class="muted">None</span>
              {:else}
                <ul class="labels">
                  {#each joinedLabels as [key, value] (key)}
                    <li class="mono">{key}={value}</li>
                  {/each}
                </ul>
              {/if}
            </dd>
          </div>
          <div class="wide">
            <dt>Backends</dt>
            <dd><BackendList backends={joinedHost.backend_info} kinds={joinedHost.backends} /></dd>
          </div>
        </dl>
      {:else}
        <p class="fine">Fetching the host’s details…</p>
      {/if}

      <div class="next">
        {#if placeable.length > 0}
          <p>
            <Check size={14} aria-hidden="true" />
            {pluralise(placeable.length, 'pool')} can place runners here: {joinWords(
              placeable.map((p) => p.name ?? p.id ?? ''),
            )}. Nothing more to do — the next queued job one of them matches may land on this host.
          </p>
        {:else if fleet.pools.length === 0}
          <p>
            No pool exists yet, so nothing tells runners to be created. A pool decides what labels
            your runners answer to and how many of them there are.
          </p>
          <Button variant="primary" href="/pools/new" iconAfter={ArrowRight}>Create a pool</Button>
        {:else}
          <p>
            No pool can place runners here yet. Each one needs a backend this host offers and the
            labels its selector names; edit the host’s labels on the Hosts page, or create a pool
            for it.
          </p>
          <Button variant="primary" href="/pools/new" iconAfter={ArrowRight}>Create a pool</Button>
        {/if}
      </div>

      <footer class="actions">
        <Button variant="secondary" icon={Plus} onclick={reset}>Add another host</Button>
        <Button variant="primary" href="/hosts" iconAfter={ArrowRight}>Go to hosts</Button>
      </footer>
    </section>
  {/if}
</div>

<style>
  .flow {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
    max-width: 880px;
  }

  /* The stepper mirrors the Wizard's, laid out in a row: three short titles. */
  .steps {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-5);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .steps li {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
  }
  .marker {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--z-space-5);
    height: var(--z-space-5);
    border: 1px solid var(--z-border-strong);
    border-radius: var(--z-radius-full);
    background: var(--z-surface);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .active .marker {
    border-color: var(--z-accent);
    background: var(--z-accent);
    color: var(--z-accent-contrast);
  }
  .done .marker {
    border-color: var(--z-idle-border);
    background: var(--z-idle-subtle);
    color: var(--z-idle);
  }
  .step-title {
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .active .step-title {
    color: var(--z-text);
  }

  .panel {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
    padding: var(--z-space-6);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  h2:focus-visible {
    outline: 2px solid var(--z-accent);
    outline-offset: 2px;
    border-radius: var(--z-radius-sm);
  }
  .lede {
    margin: calc(-1 * var(--z-space-3)) 0 0;
    max-width: 68ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
    text-wrap: pretty;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: flex-end;
    gap: var(--z-space-3);
    padding-top: var(--z-space-4);
    border-top: 1px solid var(--z-border);
  }

  .suggest {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin-bottom: var(--z-space-3);
  }
  .suggest p {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .suggest ul {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .command {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: var(--z-space-4);
    border: 1px solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
  }
  .command.small {
    padding: var(--z-space-3);
    border-color: var(--z-border);
    background: var(--z-surface-sunken);
  }
  .command pre {
    margin: 0;
    padding: var(--z-space-3);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface);
    color: var(--z-text);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .command.small pre {
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .command-actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-3);
  }
  .fine {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }

  .alternatives summary {
    cursor: pointer;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .alternatives > p {
    margin: var(--z-space-3) 0 var(--z-space-2);
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .token-line {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
  }
  .token-line code {
    padding: 0 var(--z-space-1);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-xs);
    overflow-wrap: anywhere;
  }

  /* The waiting view. Pending until something happens, in words and in colour. */
  .watch {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-4);
    border: 1px solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
  }
  .watch.expired {
    border-color: var(--z-danger-border);
    background: var(--z-danger-subtle);
  }
  .watch-text {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .watch-text p {
    margin: 0;
    max-width: 68ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
    text-wrap: pretty;
  }
  .pulse {
    flex: none;
    width: var(--z-space-3);
    height: var(--z-space-3);
    margin-top: var(--z-space-1);
    border-radius: var(--z-radius-full);
    background: var(--z-pending);
    animation: breathe calc(var(--z-motion-slow) * 5) var(--z-ease) infinite alternate;
  }
  @keyframes breathe {
    from {
      opacity: 0.35;
      transform: scale(0.8);
    }
    to {
      opacity: 1;
      transform: scale(1);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .pulse {
      animation: none;
    }
  }

  .facts {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
    gap: var(--z-space-4);
    margin: 0;
  }
  .facts .wide {
    grid-column: 1 / -1;
  }
  .facts dt {
    margin: 0 0 var(--z-space-1);
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-text-muted);
    font-weight: var(--z-weight-medium);
  }
  .facts dd {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .muted {
    color: var(--z-text-subtle);
  }
  .labels {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .labels li {
    padding: 0 var(--z-space-1);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
  }

  .next {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-4);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .next p {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: 0;
    max-width: 68ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
    text-wrap: pretty;
  }

  @media (max-width: 768px) {
    .panel {
      padding: var(--z-space-4);
    }
    .actions {
      flex-direction: column-reverse;
      align-items: stretch;
    }
  }
</style>

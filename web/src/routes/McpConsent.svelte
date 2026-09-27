<!--
  Connect an MCP client.

  Claude -- or any MCP client -- sends somebody here from another app to
  approve a connection to this controller. By the time this renders they have
  signed in (the shell shows the sign-in form in place first), and the page is
  one decision: who is asking, where the answer goes, and what the connection
  may do.

  The redirect host is shown plainly because it is the one thing about the
  client the controller has checked, and a client whose every redirect is on
  this computer gets a warning of its own: any program running here can listen
  on a loopback port and claim to be it.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { Cable, Laptop, ShieldCheck, TriangleAlert } from '@lucide/svelte';
  import { ApiError, approveMCPRequest, denyMCPRequest, getMCPRequest } from '$lib/api/client';
  import type { MCPConsent, Role } from '$lib/api/types';
  import { router } from '$lib/router';
  import { roleLabel } from '$lib/roles';
  import { session } from '$lib/state/session.svelte';
  import Button from '$lib/components/Button.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  const ROLE_TEXT: Record<string, string> = {
    viewer: 'Read jobs, runners, pools, hosts, problems and runner logs.',
    operator: 'Also re-run a failed job and drain an idle runner.',
  };

  /* A refusal the controller made before there was anything to decide --
     an unknown client, a redirect it never registered -- arrives in the
     address, and is read once. */
  const refusal = untrack(() => ({
    code: router.param('error'),
    description: router.param('error_description'),
  }));
  const requestId = untrack(() => router.param('request'));

  let consent = $state<MCPConsent | null>(null);
  let loading = $state(!refusal.code && requestId !== '');
  let failure = $state<unknown>(null);
  let role = $state<string>('viewer');
  let deciding = $state<'approve' | 'deny' | null>(null);
  let leavingFor = $state('');

  $effect(() => {
    if (refusal.code || !requestId) return;
    const controller = new AbortController();
    void getMCPRequest(requestId, controller.signal)
      .then((view) => {
        consent = view;
        role = view.suggested_role;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        failure = cause;
      })
      .finally(() => (loading = false));
    return () => controller.abort();
  });

  $effect(() => {
    router.setTitle(consent ? `Connect ${consent.client.name}` : 'Connect an MCP client');
  });

  const options = $derived(
    (consent?.roles ?? []).map((r) => ({
      value: r,
      label: roleLabel(r),
      description: ROLE_TEXT[r] ?? '',
    })),
  );

  const kindText = $derived.by(() => {
    if (!consent) return '';
    switch (consent.client.kind) {
      case 'admin':
        return 'An administrator of this controller created this client.';
      case 'metadata':
        return `It identified itself with the document at ${consent.client.client_id}.`;
      default:
        return 'It registered itself with this controller when it connected. Its name is its own description; where it sends you is what has been checked.';
    }
  });

  async function decide(approve: boolean): Promise<void> {
    if (!consent || deciding) return;
    deciding = approve ? 'approve' : 'deny';
    failure = null;
    try {
      const d = approve
        ? await approveMCPRequest(consent.id, { role: role as Role })
        : await denyMCPRequest(consent.id);
      leavingFor = consent.redirect_host;
      // Leaving the app for the client's own address: a full navigation,
      // not the router, because it is somewhere else entirely.
      location.assign(d.redirect_to);
    } catch (cause) {
      failure = cause;
      deciding = null;
    }
  }

  const expired = $derived(failure instanceof ApiError && failure.status === 404);
</script>

<div class="consent">
  <div class="brand"><Logo variant="full" size={28} /></div>

  {#if refusal.code}
    <section class="card" aria-labelledby="page-heading">
      <h1 id="page-heading" tabindex="-1">This connection cannot go ahead</h1>
      <p class="failure" role="alert">
        <TriangleAlert size={16} aria-hidden="true" />
        <span
          >{refusal.description ||
            'The app that sent you here asked for something this controller will not do.'}</span
        >
      </p>
      <p class="muted">
        Nothing was shared and you have not been sent anywhere else. Go back to the app you came
        from and try connecting again; if it keeps happening, the app's connector settings need
        changing. The code was <span class="mono">{refusal.code}</span>.
      </p>
      <div class="actions"><Button href="/">Go to Zoomies</Button></div>
    </section>
  {:else if !requestId}
    <section class="card" aria-labelledby="page-heading">
      <h1 id="page-heading" tabindex="-1">Nothing to approve</h1>
      <p class="muted">
        This page is where an app such as Claude sends you to approve a connection to Zoomies. Start
        from the app: add a connector with this controller's <span class="mono">/mcp</span> address.
      </p>
      <div class="actions"><Button href="/">Go to Zoomies</Button></div>
    </section>
  {:else if loading}
    <section class="card" aria-busy="true" aria-label="Loading the request">
      <Skeleton width="60%" height="1.75rem" />
      <Skeleton lines={3} />
      <Skeleton height="120px" />
    </section>
  {:else if expired || (!consent && failure)}
    <section class="card" aria-labelledby="page-heading">
      {#if expired}
        <h1 id="page-heading" tabindex="-1">This request has expired</h1>
        <p class="muted">
          It was answered already, or it waited more than fifteen minutes. Go back to the app you
          came from and connect again.
        </p>
      {:else}
        <ErrorState
          error={failure}
          title="The request could not be loaded"
          onretry={() => location.reload()}
        />
      {/if}
    </section>
  {:else if consent}
    <section class="card" aria-labelledby="page-heading">
      <div class="icon" aria-hidden="true"><Cable size={22} /></div>
      <h1 id="page-heading" tabindex="-1">
        Connect <span class="client">{consent.client.name}</span> to Zoomies
      </h1>
      <p class="lede">
        It is asking to use this fleet's MCP tools as you, <strong>{session.displayName}</strong>.
      </p>

      <dl class="facts">
        <div>
          <dt>Sends you back to</dt>
          <dd><span class="mono host">{consent.redirect_host}</span></dd>
        </div>
        <div>
          <dt>About this app</dt>
          <dd>{kindText}</dd>
        </div>
      </dl>

      {#if consent.loopback_only}
        <p class="warning" role="note">
          <Laptop size={16} aria-hidden="true" />
          <span>
            Every address this app registered is on <strong>this computer</strong>. Any program
            running here could claim to be it, so continue only if you started this yourself, from
            an app you trust such as Claude Code.
          </span>
        </p>
      {/if}

      <RadioGroup bind:value={role} name="connection-role" legend="What it may do" {options} />
      {#if consent.roles.length === 1}
        <p class="hint">
          Your own role is {roleLabel(consent.your_role).toLowerCase()}, so this is the most you can
          give it.
        </p>
      {/if}

      <p class="scope">
        <ShieldCheck size={16} aria-hidden="true" />
        <span>
          It works on <span class="mono">/mcp</span> only — not the API or this interface — and never
          with more than your own role. Disconnect it any time under Settings, MCP connections.
        </span>
      </p>

      {#if failure}
        <p class="failure" role="alert">
          <TriangleAlert size={16} aria-hidden="true" />
          <span>{failure instanceof Error ? failure.message : 'That did not work; try again.'}</span
          >
        </p>
      {/if}

      {#if leavingFor}
        <p class="leaving" role="status">Taking you back to {leavingFor}…</p>
      {/if}

      <div class="actions">
        <Button
          variant="secondary"
          loading={deciding === 'deny'}
          disabled={deciding !== null}
          onclick={() => void decide(false)}>Deny</Button
        >
        <Button
          variant="primary"
          loading={deciding === 'approve'}
          disabled={deciding !== null}
          onclick={() => void decide(true)}>Allow</Button
        >
      </div>
    </section>
  {/if}
</div>

<style>
  .consent {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--z-space-5);
    width: 100%;
    max-width: var(--z-width-dialog-md);
  }
  .brand {
    color: var(--z-text);
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    width: 100%;
    padding: var(--z-space-8);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-lg);
    background: var(--z-surface);
    box-shadow: var(--z-shadow-md);
  }
  .icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--z-space-10);
    height: var(--z-space-10);
    border-radius: var(--z-radius-full);
    background: var(--z-accent-subtle);
    color: var(--z-accent);
  }
  h1 {
    margin: 0;
    font-size: var(--z-text-xl);
    line-height: var(--z-leading-xl);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  h1:focus {
    outline: none;
  }
  .lede,
  .muted,
  .hint {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .hint {
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    margin-top: calc(-1 * var(--z-space-2));
  }
  .lede strong {
    color: var(--z-text);
    font-weight: var(--z-weight-medium);
  }
  .facts {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    margin: 0;
    padding: var(--z-space-4);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .facts dt {
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    letter-spacing: var(--z-tracking-wide);
    text-transform: uppercase;
    color: var(--z-text-muted);
  }
  .facts dd {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .host {
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-medium);
  }
  .mono {
    font-family: var(--z-font-mono);
  }
  .warning,
  .failure,
  .scope {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: 0;
    padding: var(--z-space-3);
    border-radius: var(--z-radius-md);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
  .warning,
  .failure {
    border: var(--z-border-width) solid var(--z-danger-border);
    background: var(--z-danger-subtle);
  }
  .scope {
    border: var(--z-border-width) solid var(--z-border);
    color: var(--z-text-muted);
  }
  .warning :global(svg),
  .failure :global(svg),
  .scope :global(svg) {
    flex: none;
    margin-top: var(--z-nudge-2);
  }
  .warning :global(svg),
  .failure :global(svg) {
    color: var(--z-danger);
  }
  .leaving {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--z-space-3);
    margin-top: var(--z-space-2);
  }
  @media (max-width: 768px) {
    .card {
      padding: var(--z-space-5);
    }
    .actions {
      flex-direction: column-reverse;
    }
    .actions :global(> *) {
      width: 100%;
    }
  }
</style>

<!--
  A command that reads this host's health from a terminal, ready to paste.

  `zoomies doctor --host` asks the controller, and so needs the controller's
  address and, unless authentication is off, a token. Left for somebody to fill
  in, that is a command that fails on the first machine it is tried on -- and the
  machine it is tried on is usually the host itself, which knows its own
  controller and nobody's token. So the page makes both: the address is the one
  it already hands out for adding a host, and the token is made on the click,
  read-only, for hosts alone, and over in a quarter of an hour.

  The token is shown once, here, and not stored by the page. When it ends the
  command goes with it, so a dead token is never what gets copied.
-->
<script lang="ts">
  import { KeyRound, Terminal } from '@lucide/svelte';
  import { createToken } from '$lib/api/client';
  import { suggestedControllerURL } from '$lib/addresses';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import { onClockTick } from '$lib/format';
  import { doctorCommand } from '$lib/hosts/terminal';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';

  interface Props {
    hostId: string;
  }

  let { hostId }: Props = $props();

  /** How long the token lasts. The server takes any duration; the page promises this one. */
  const LIFETIME = '15m';

  const url = $derived(suggestedControllerURL(session.meta?.external_url, location.origin));

  let token = $state<string | null>(null);
  let expiresAt = $state<string | null>(null);
  let minting = $state(false);
  let now = $state(Date.now());
  // The shared clock catches a tab that was in the background up with the time,
  // and the timer ends the token at the second it ends, not at the next tick: a
  // command that outlives its token by up to ten seconds is one somebody copies.
  $effect(() => onClockTick((t) => (now = t)));
  $effect(() => {
    if (!expiresAt) return;
    const wait = Math.max(Date.parse(expiresAt) - Date.now(), 0);
    const timer = setTimeout(() => (now = Date.now()), wait + 20);
    return () => clearTimeout(timer);
  });

  const live = $derived(token !== null && expiresAt !== null && Date.parse(expiresAt) > now);
  const expired = $derived(token !== null && !live);
  // With authentication off the controller asks nobody who they are, and a token
  // would be refused for having no account behind it; the command needs none.
  const needsToken = $derived(!session.authDisabled);
  const command = $derived(
    needsToken
      ? live && token
        ? doctorCommand(hostId, url, token)
        : ''
      : doctorCommand(hostId, url),
  );

  async function make(): Promise<void> {
    minting = true;
    try {
      const made = await createToken({
        // The host's id and not its name: the name is whatever the host calls
        // itself, and this one is listed in the account's tokens until it is purged.
        name: `Terminal: doctor ${hostId}`,
        role: 'viewer',
        scopes: ['hosts:read'],
        expires_in: LIFETIME,
      });
      token = made.token ?? null;
      expiresAt = made.expires_at ?? null;
    } catch (cause) {
      toasts.fromError(cause, 'No command was made');
    } finally {
      minting = false;
    }
  }
</script>

<div class="terminal">
  {#if !needsToken}
    <p class="note">This controller has authentication off, so the command needs no token.</p>
  {:else if !live}
    <p class="note">
      {#if expired}
        That token has ended. Make another for a new command.
      {:else}
        Makes a token that can only read hosts and stops working in 15 minutes, and a command with
        it and this controller's address already in.
      {/if}
    </p>
    <div>
      <Button icon={KeyRound} loading={minting} onclick={() => void make()}>
        {expired ? 'Make another command' : 'Make a command'}
      </Button>
    </div>
  {/if}

  {#if command}
    <pre class="mono"><code>{command}</code></pre>
    <div class="actions">
      <CopyButton value={command} label="Copy the command" size="md" showLabel />
      {#if needsToken && expiresAt}
        <span class="fine">
          The token in it ends <RelativeTime value={expiresAt} />, can only read hosts, and is not
          shown again.
        </span>
      {/if}
    </div>
  {/if}
  <p class="fine">
    <Terminal size={14} aria-hidden="true" /> On this host itself,
    <code>zoomies doctor --verbose</code> reads it directly and needs none of this.
  </p>
</div>

<style>
  .terminal {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    align-items: flex-start;
  }
  .note,
  .fine {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .fine {
    font-size: var(--z-text-xs);
  }
  pre {
    align-self: stretch;
    margin: 0;
    padding: var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    color: var(--z-text);
    font-size: var(--z-text-xs);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .actions {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-3);
  }
</style>

<!--
  Removing a host.

  The API refuses while the host still has live runners, and its refusal names
  them and says what to do. That message is shown verbatim rather than
  translated, and only then is forcing offered -- with what forcing actually
  means spelled out, since the runners are not stopped by it, they are simply
  no longer anybody's responsibility here.
-->
<script lang="ts">
  import { ApiError, deleteHost } from '$lib/api/client';
  import type { Host } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Dialog from '$lib/components/Dialog.svelte';

  interface Props {
    open?: boolean;
    host: Host | null;
    onclose?: () => void;
  }

  let { open = $bindable(false), host, onclose }: Props = $props();

  /** The server's refusal, kept as it was written. */
  let refusal = $state('');
  let forcing = $state(false);

  const name = $derived(host?.name || host?.id || 'this host');
  const runners = $derived(host?.active_runners ?? 0);

  // Opening the dialog again starts from a clean slate. It deliberately does
  // not clear on close: the confirmation closes itself the moment the server
  // refuses, and the refusal is what we show next.
  $effect(() => {
    if (open) refusal = '';
  });

  async function remove(force: boolean): Promise<void> {
    if (!host?.id) return;
    try {
      await deleteHost(host.id, force ? { force: true } : undefined);
      await fleet.reconcile();
      toasts.success(`${name} removed`, force ? 'It was removed while runners were live.' : '');
      refusal = '';
      open = false;
      onclose?.();
    } catch (cause) {
      if (cause instanceof ApiError && cause.isConflict) {
        // Not an error to shout about: it is the guard working. Offer the way past it.
        refusal = cause.message;
        return;
      }
      toasts.fromError(cause, 'That host was not removed');
    }
  }

  function close(): void {
    open = false;
    refusal = '';
    onclose?.();
  }
</script>

{#if refusal}
  <Dialog open={true} title="Still running work" size="sm" dismissible={false} onclose={close}>
    <p class="message">{refusal}</p>
    <p class="consequence">
      Forcing it removes {name} from Zoomies now. Anything still running on it keeps running until its
      own agent cleans it up, and those runners stay registered with GitHub until they exit.
    </p>

    {#snippet footer()}
      <Button variant="ghost" onclick={close}>Leave it alone</Button>
      <Button
        variant="danger"
        loading={forcing}
        onclick={async () => {
          forcing = true;
          await remove(true);
          forcing = false;
        }}
      >
        Remove it anyway
      </Button>
    {/snippet}
  </Dialog>
{:else}
  <ConfirmDialog
    bind:open
    title="Remove host"
    name={host?.name || host?.id}
    description="{name} will be removed from this controller. Its agent will have to enrol again with a new join token to come back."
    consequences={[
      runners > 0
        ? `${pluralise(runners, 'runner')} on this host will be refused: cordon it and let them finish first.`
        : 'No runners are on this host right now.',
      'Any join token already used for this host stays spent.',
    ]}
    confirmLabel="Remove host"
    requireName
    onconfirm={() => remove(false)}
    oncancel={onclose}
  />
{/if}

<style>
  .message {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
  }
  .consequence {
    margin: var(--z-space-3) 0 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
</style>

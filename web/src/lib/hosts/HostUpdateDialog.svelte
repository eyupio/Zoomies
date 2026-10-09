<!--
  The last thing between a press and an agent restarting.

  A confirmation in the default tone and not the danger one: an update is the
  product doing what it was installed to do, and nothing here is lost. It still
  says what follows, in `update.ts`, so that the tests and this dialog say the
  same thing about the restart.
-->
<script lang="ts">
  import type { Host } from '$lib/api/types';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { requestUpdate } from './actions';
  import { confirmHostUpdate, targetTag } from './update';

  interface Props {
    open?: boolean;
    host: Host | null;
    onclose?: () => void;
  }

  let { open = $bindable(false), host, onclose }: Props = $props();

  const words = $derived(
    confirmHostUpdate(
      host?.name || host?.id || 'this host',
      host?.version ?? '',
      targetTag(host ?? {}),
    ),
  );

  // After a press the button is gone and the dialog's opener with it, so focus
  // would fall to the page. It goes to the row that replaced the button. The
  // dialog gives focus back a frame or two after it closes, and finding its
  // opener gone sends it to the page heading; so this asks after those frames,
  // and once more a moment later if focus has still been left on the page.
  function landOn(id: string): void {
    requestAnimationFrame(() =>
      requestAnimationFrame(() => {
        const target = document.getElementById(id);
        target?.focus();
        setTimeout(() => {
          const held = document.activeElement;
          if (!held || held === document.body || held.id === 'page-heading')
            document.getElementById(id)?.focus();
        }, 250);
      }),
    );
  }

  async function confirm(): Promise<boolean> {
    if (host) {
      const taken = await requestUpdate(host);
      if (taken && host.id) landOn(`host-${host.id}-update`);
    }
    // Closed either way: the dialog has nothing more to ask. A refusal is a
    // toast in the controller's words, and the card is as it was.
    onclose?.();
    return true;
  }
</script>

<ConfirmDialog
  bind:open
  title={words.title}
  description={words.description}
  consequences={words.consequences}
  confirmLabel={words.confirmLabel}
  tone="default"
  oncancel={onclose}
  onconfirm={confirm}
/>

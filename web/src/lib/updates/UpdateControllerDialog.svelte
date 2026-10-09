<!--
  The last thing between a click and a restart.

  It is a confirmation in the default tone and not the danger one: an update is
  the product doing what it was installed to do. It still counts what follows,
  because the controller going away for a minute is what the operator is
  agreeing to, and the words are in `words.ts` so that the page's tests and this
  dialog say the same thing.
-->
<script lang="ts">
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { confirmControllerUpdate } from './words';

  interface Props {
    open?: boolean;
    /** The release the button was offered for, which is the one the request names. */
    tag: string;
    /** The build the controller reports now. */
    running: string;
    /** Return false to keep the dialog open for another try. */
    onconfirm: () => boolean | Promise<boolean>;
  }

  let { open = $bindable(false), tag, running, onconfirm }: Props = $props();

  const words = $derived(confirmControllerUpdate(tag, running));
</script>

<ConfirmDialog
  bind:open
  title={words.title}
  description={words.description}
  consequences={words.consequences}
  confirmLabel={words.confirmLabel}
  tone="default"
  {onconfirm}
/>

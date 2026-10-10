<!--
  The last thing between a click and every host behind being updated in turn.

  The default tone, as for one host: an update is the product doing what it was
  installed to do. It counts what follows (the restarts, the order, what one
  failure does) in words from `words.ts`, so that the tests and this dialog say
  the same thing.
-->
<script lang="ts">
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { confirmRollout } from './words';

  interface Props {
    open?: boolean;
    /** The controller's release, which every host is taken to. */
    tag: string;
    /** The hosts the button counted when it was pressed. */
    count: number;
    onconfirm: () => boolean | Promise<boolean>;
    oncancel?: () => void;
  }

  let { open = $bindable(false), tag, count, onconfirm, oncancel }: Props = $props();

  const words = $derived(confirmRollout(tag, count));
</script>

<ConfirmDialog
  bind:open
  title={words.title}
  description={words.description}
  consequences={words.consequences}
  confirmLabel={words.confirmLabel}
  tone="default"
  {oncancel}
  {onconfirm}
/>

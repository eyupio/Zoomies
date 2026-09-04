<!--
  Destructive confirmation.

  It names the thing, counts what it affects, and says out loud when runners are
  removed from GitHub. Anything irreversible makes you type the name: a habit
  that costs three seconds and has saved entire fleets.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import Button from './Button.svelte';
  import Dialog from './Dialog.svelte';
  import Field from './Field.svelte';
  import Input from './Input.svelte';

  interface Props {
    open?: boolean;
    /** "Delete pool" -- the verb and the kind of thing. */
    title: string;
    /** The name of the specific thing. Shown in the sentence and typed to confirm. */
    name?: string;
    /** What will happen, in one sentence. */
    description: string;
    /** The counted consequences: "3 runners will be drained". */
    consequences?: readonly string[];
    confirmLabel?: string;
    cancelLabel?: string;
    /** Require the operator to type `name`. Use for anything that cannot be undone. */
    requireName?: boolean;
    tone?: 'danger' | 'default';
    busy?: boolean;
    onconfirm: () => void | Promise<void>;
    oncancel?: () => void;
    /** Extra controls -- a "remove from GitHub too" switch, say. */
    children?: Snippet;
  }

  let {
    open = $bindable(false),
    title,
    name,
    description,
    consequences,
    confirmLabel = 'Delete',
    cancelLabel = 'Cancel',
    requireName = false,
    tone = 'danger',
    busy = false,
    onconfirm,
    oncancel,
    children,
  }: Props = $props();

  let typed = $state('');
  let running = $state(false);

  const needsTyping = $derived(requireName && Boolean(name));
  const confirmed = $derived(!needsTyping || typed.trim() === name);

  $effect(() => {
    if (!open) typed = '';
  });

  function cancel(): void {
    open = false;
    oncancel?.();
  }

  async function confirm(): Promise<void> {
    if (!confirmed || running) return;
    running = true;
    try {
      await onconfirm();
      open = false;
    } finally {
      running = false;
    }
  }
</script>

<Dialog bind:open {title} size="sm" dismissible={false} onclose={oncancel}>
  <p class="description">
    {description}
  </p>
  {#if consequences && consequences.length > 0}
    <ul class="consequences">
      {#each consequences as line (line)}
        <li>{line}</li>
      {/each}
    </ul>
  {/if}
  {#if children}
    <div class="extra">{@render children()}</div>
  {/if}
  {#if needsTyping}
    <div class="confirm-field">
      <Field label="Type {name} to confirm" hint="This cannot be undone.">
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={typed}
            {id}
            {describedBy}
            {invalid}
            mono
            autocomplete="off"
            ariaLabel="Type the name to confirm"
          />
        {/snippet}
      </Field>
    </div>
  {/if}

  {#snippet footer()}
    <Button variant="ghost" onclick={cancel} disabled={running || busy}>{cancelLabel}</Button>
    <Button
      variant={tone === 'danger' ? 'danger' : 'primary'}
      onclick={confirm}
      disabled={!confirmed}
      loading={running || busy}
    >
      {confirmLabel}
    </Button>
  {/snippet}
</Dialog>

<style>
  .description {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
  }
  .consequences {
    margin: var(--z-space-3) 0 0;
    padding-left: var(--z-space-5);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .consequences li + li {
    margin-top: var(--z-space-1);
  }
  .extra {
    margin-top: var(--z-space-4);
  }
  .confirm-field {
    margin-top: var(--z-space-4);
  }
</style>

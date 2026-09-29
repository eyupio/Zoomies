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
  import { toasts } from '../state/toasts.svelte';

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
    /** Return false to keep the confirmation and its input available for retry. */
    onconfirm: () => boolean | Promise<boolean>;
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

  const uid = $props.id();
  const formId = `${uid}-confirm`;

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
      if (await onconfirm()) open = false;
    } catch (cause) {
      toasts.fromError(cause, 'That action was not completed');
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
    <!--
      A form only around the typed name, so Enter in it confirms. Not around the
      whole body: Enter in a checkbox a caller puts in `children` must not be
      able to fire the action. The submit button is disabled until the name
      matches, which is what stops Enter on a wrong name doing anything.
    -->
    <form
      id={formId}
      class="confirm-field"
      novalidate
      onsubmit={(event) => {
        event.preventDefault();
        void confirm();
      }}
    >
      <!--
        No ariaLabel here. One would win over the label Field wires up, so the
        only control guarding an irreversible change would be announced as
        "type the name to confirm" without ever saying which name -- and the
        name is the whole of what this field asks for. The visible label is the
        accessible name, which is also what stops voice control from being told
        one thing while the screen says another.
      -->
      <Field label="Type {name} to confirm" hint="This cannot be undone.">
        {#snippet children({ id, describedBy, invalid })}
          <Input bind:value={typed} {id} {describedBy} {invalid} mono autocomplete="off" />
        {/snippet}
      </Field>
    </form>
  {/if}

  {#snippet footer()}
    <Button variant="ghost" onclick={cancel} disabled={running || busy}>{cancelLabel}</Button>
    <Button
      variant={tone === 'danger' ? 'danger' : 'primary'}
      type={needsTyping ? 'submit' : 'button'}
      form={needsTyping ? formId : undefined}
      onclick={needsTyping ? undefined : confirm}
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

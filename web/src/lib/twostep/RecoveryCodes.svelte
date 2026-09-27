<!--
  Ten recovery codes, shown once.

  The server kept only their hashes, so this is the only moment they exist
  anywhere a person can read them. The component says so, lays them out to be
  copied or written down -- two columns, monospaced, numbered so a person
  crossing them off paper can say which one they used -- and offers the two
  ways people actually keep them: a copy into a password manager, or a file.
-->
<script lang="ts">
  import { Download, ShieldCheck } from '@lucide/svelte';
  import { saveBlob } from '$lib/settings/backups';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';

  interface Props {
    codes: readonly string[];
    /** The account they belong to, for the file's name and first line. */
    account?: string;
  }

  let { codes, account = '' }: Props = $props();

  const text = $derived(codes.join('\n'));

  function download(): void {
    const heading = `Zoomies recovery codes${account ? ` for ${account}` : ''} (${location.host})`;
    const body = `${heading}\nEach code works once, in place of a code from your authenticator app.\n\n${text}\n`;
    saveBlob(
      new Blob([body], { type: 'text/plain' }),
      `zoomies-recovery-codes${account ? `-${account}` : ''}.txt`,
    );
  }
</script>

<section class="codes" aria-labelledby="recovery-heading">
  <p class="heading" id="recovery-heading">
    <ShieldCheck size={16} aria-hidden="true" />
    <span>Save your recovery codes now</span>
  </p>
  <p class="explain">
    Each one signs you in once if you lose your phone. They are shown only this once: keep them
    somewhere that is not your phone, such as a password manager.
  </p>
  <ol class="list mono" aria-label="Recovery codes">
    {#each codes as code (code)}
      <li>{code}</li>
    {/each}
  </ol>
  <div class="actions">
    <CopyButton value={text} label="Copy the codes" size="md" showLabel />
    <Button variant="secondary" icon={Download} onclick={download}>Download</Button>
  </div>
</section>

<style>
  .codes {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
  }
  .heading {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .explain {
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  .list {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--z-space-2) var(--z-space-6);
    margin: 0;
    padding: var(--z-space-3) var(--z-space-3) var(--z-space-3) var(--z-space-8);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface);
    color: var(--z-text);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
  }
  .list li::marker {
    color: var(--z-text-subtle);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-3);
  }
</style>

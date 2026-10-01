<!--
  The way out of "GitHub cannot reach Zoomies".

  A GitHub App's webhook URL is fixed when GitHub creates the App, and it is
  built from server.external_url, so the connect dialog will not build a
  manifest while that is empty or an address only this machine can use.
  Refusing was right. Refusing with "edit the configuration, restart something,
  and come back" was not: the terminal installer offers three ways out of the
  same wall, and the browser offered none, on the one button a first-time
  operator on a default install has to press.

  This is the way out, where it is needed. Write the address, save it through
  the call the Settings page makes, and be told the one thing no page can do for
  the operator -- start the process again. The setting is read at startup, so
  the dialog then watches the controller's own answer and carries on by itself
  when it changes.

  Who may do this is the platform's business, not the fleet's: the key belongs
  to whoever runs the process, so an administrator below that role is not
  shown it at all. They are told whose job it is rather than offered a form
  that can only refuse.
-->
<script lang="ts">
  import { CircleAlert, ExternalLink } from '@lucide/svelte';
  import { ApiError, getSettings, updateSettings } from '$lib/api/client';
  import { isLoopbackURL } from '$lib/addresses';
  import { EXTERNAL_URL_URL, HOME_LAB_URL } from '$lib/links';
  import { session } from '$lib/state/session.svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';

  interface Props {
    /** What the controller believes it is reached at: nothing, or an address only it can use. */
    current: string;
  }

  let { current }: Props = $props();

  const KEY = 'server.external_url';
  /** How often a saved address is checked for, once the controller has been asked to restart. */
  const WATCH_MS = 3000;

  /**
   * - `loading`: asking the setting what it says about itself.
   * - `edit`: the form.
   * - `waiting`: an address is saved and the process has to start again to use it.
   * - `refused`: it cannot be changed from here, and `why` says who can.
   */
  type Phase = 'loading' | 'edit' | 'waiting' | 'refused';

  const mayChange = session.can('platform');
  let phase = $state<Phase>(mayChange ? 'loading' : 'refused');
  let why = $state(
    mayChange
      ? ''
      : 'This address belongs to whoever runs the controller rather than to the fleet, so changing it needs the Platform role and this account does not have it. Ask them to set it to the address GitHub can reach; the controller restarts to apply it.',
  );

  /*
    Look before offering a form. An address the environment is pinning would be
    stored and then overridden at the next restart, so the API refuses it, and a
    form that can only produce that refusal is worse than the sentence the API
    already wrote for exactly this case. A value saved earlier and still waiting
    for a restart goes straight to the instruction that matters.
  */
  $effect(() => {
    if (phase !== 'loading') return;
    const request = new AbortController();
    getSettings(request.signal).then(
      (all) => {
        const row = all.settings?.find((setting) => setting.key === KEY);
        if (row && row.editable === false) {
          why = row.reason ?? 'It cannot be changed from here.';
          phase = 'refused';
        } else {
          phase = row?.pending ? 'waiting' : 'edit';
        }
      },
      () => {
        // The save is the authority. A failed look is no reason to hide the form.
        if (!request.signal.aborted) phase = 'edit';
      },
    );
    return () => request.abort();
  });

  /* -- the form ------------------------------------------------------------- */

  /**
   * The address this browser is using is the best first guess there is: it is
   * the one the operator actually typed, and the reason they could reach the
   * controller at all. When it is loopback too -- an SSH tunnel to localhost --
   * it is no guess, so the field starts empty.
   */
  let address = $state(isLoopbackURL(location.origin) ? '' : location.origin);
  let busy = $state(false);
  let failure = $state('');
  let saved = $state('');

  const problem = $derived.by(() => {
    const raw = address.trim();
    if (raw === '') return '';
    let parsed: URL;
    try {
      parsed = new URL(raw);
    } catch {
      return 'Write it like https://zoomies.example.com.';
    }
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return 'Start it with http:// or https://.';
    }
    if (isLoopbackURL(raw))
      return 'Only this machine can use that address. GitHub needs one it can reach.';
    return '';
  });

  async function save(): Promise<void> {
    const value = address.trim().replace(/\/+$/, '');
    if (!value || problem || busy) return;
    busy = true;
    failure = '';
    try {
      const result = await updateSettings({ [KEY]: value });
      saved = value;
      // The setting says whether the process can apply it to itself, so this
      // follows what it says rather than assuming.
      if (result.settings?.find((setting) => setting.key === KEY)?.live) {
        await session.reloadMeta();
      } else {
        phase = 'waiting';
      }
    } catch (cause) {
      if (cause instanceof ApiError) {
        const fields = cause.fieldErrors();
        failure = fields[KEY] ?? (Object.values(fields).filter(Boolean).join(' ') || cause.message);
      } else {
        failure =
          'The address could not be saved. Check that the controller is still running, then try again.';
      }
    } finally {
      busy = false;
    }
  }

  /*
    Once the controller has been asked to restart, ask it what it now says. A
    failure keeps the answer already held (reloadMeta swallows it), so the
    seconds when the process is down are quiet; the first answer that carries
    the new address makes the dialog's `notReachable` false and this panel is
    replaced by the form it was standing in front of.
  */
  $effect(() => {
    if (phase !== 'waiting') return;
    const timer = setInterval(() => void session.reloadMeta(), WATCH_MS);
    return () => clearInterval(timer);
  });

  const title = $derived(
    phase === 'waiting'
      ? 'Saved — restart the controller to use it'
      : current === ''
        ? 'Zoomies has no external URL yet'
        : 'GitHub cannot reach Zoomies',
  );
</script>

<section class="blocked" aria-labelledby="external-address-title">
  <CircleAlert size={16} aria-hidden="true" />
  <div class="body">
    <p id="external-address-title" class="title">{title}</p>

    {#if phase === 'waiting'}
      <p role="status">
        {saved ? `Zoomies has saved ${saved}.` : 'An address is saved.'} It reads the address when it
        starts, so restart the controller on the machine it runs on. This dialog carries on by itself
        once the controller is back.
      </p>
      <dl class="commands">
        <dt>Docker or Docker Compose</dt>
        <dd>
          <code>zoomies deployment restart</code>
          <CopyButton value="zoomies deployment restart" label="Copy the restart command" />
        </dd>
        <dt>Installed as a service</dt>
        <dd>
          <code>sudo systemctl restart zoomies</code>
          <CopyButton value="sudo systemctl restart zoomies" label="Copy the restart command" />
        </dd>
      </dl>
      <Button variant="ghost" size="sm" onclick={() => (phase = 'edit')}>Change the address</Button>
    {:else}
      <p>
        {#if current === ''}
          GitHub is told where to deliver webhooks when the App is created, and that address cannot
          be changed from here afterwards.
        {:else}
          Zoomies believes it is reached at <code>{current}</code>, which is an address only this
          machine has. GitHub is told where to deliver webhooks when the App is created, and that
          address cannot be changed from here afterwards — so an App created now would carry one
          that never fires.
        {/if}
      </p>

      {#if phase === 'edit'}
        <form
          id="external-address"
          class="fix"
          onsubmit={(event) => {
            event.preventDefault();
            void save();
          }}
        >
          <Field
            label="Public address of this controller"
            hint="The address GitHub can reach it on, https if you can, such as https://zoomies.example.com."
            error={failure || problem}
          >
            {#snippet children({ id, describedBy, invalid })}
              <Input
                bind:value={address}
                {id}
                {describedBy}
                {invalid}
                type="url"
                mono
                autocomplete="off"
                placeholder="https://zoomies.example.com"
              />
            {/snippet}
          </Field>
          <Button
            type="submit"
            variant="primary"
            loading={busy}
            disabled={!address.trim() || Boolean(problem)}
          >
            Save address
          </Button>
        </form>
        <p class="aside">
          No address yet? A tunnel gives a controller at home one without opening a port:
          <a href={HOME_LAB_URL} target="_blank" rel="noopener noreferrer">
            how to set one up<ExternalLink size={12} aria-hidden="true" /><span class="sr-only">
              (opens in a new tab)</span
            >
          </a>
        </p>
      {:else if phase === 'refused'}
        <p>{why}</p>
        <p class="aside">
          <a href={EXTERNAL_URL_URL} target="_blank" rel="noopener noreferrer">
            What the setting does<ExternalLink size={12} aria-hidden="true" /><span class="sr-only">
              (opens in a new tab)</span
            >
          </a>
        </p>
      {/if}
    {/if}
  </div>
</section>

<style>
  /* The same amber as the notice this replaced: it is still a "not yet", and
     the status colours are a fixed mapping. */
  .blocked {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-3);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-pending-subtle);
    color: var(--z-text);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
  }
  .blocked > :global(svg) {
    flex: none;
    margin-top: var(--z-nudge-2);
    color: var(--z-pending);
  }
  .body {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-2);
    min-width: 0;
    flex: 1;
  }
  .body p {
    margin: 0;
    text-wrap: pretty;
  }
  .title {
    font-weight: var(--z-weight-medium);
  }
  .body code {
    font-family: var(--z-font-mono);
    word-break: break-all;
  }
  .fix {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: var(--z-space-3);
    align-self: stretch;
    margin-top: var(--z-space-1);
  }
  .fix :global(button) {
    align-self: flex-start;
  }
  .aside {
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .aside a {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    color: var(--z-accent);
  }
  .commands {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: var(--z-space-1);
    margin: 0;
    font-size: var(--z-text-xs);
  }
  .commands dt {
    color: var(--z-text-muted);
  }
  .commands dd {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: 0 0 var(--z-space-2);
    min-width: 0;
  }
</style>

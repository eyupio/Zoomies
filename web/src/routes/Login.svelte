<!--
  Sign in.

  What this renders is decided by `/meta`, which is safe to call before anyone
  is authenticated: a password form, an SSO button, both, or -- when
  authentication has been switched off in the configuration -- a plain statement
  of that fact rather than a form that would do nothing.

  This is the first screen anyone sees, and often the only one they see while
  something is wrong, so it does the small things properly: the cursor starts in
  the first empty field, caps lock is called out before it costs an attempt, the
  password can be revealed, and a failure says which kind of failure it was --
  wrong credentials, too many attempts, or a controller that cannot be reached
  at all. Those are three different problems and only one of them is the
  operator's fault.

  It is also the one page of a controller that people who did not install it
  see, so it is split in two: the form, and a Zoomies Black panel that says
  what this is, who makes it and where the source lives. The panel is the
  brand's own ground in both themes, which is what lets the lockup sit on it
  with no holding shape of its own.
-->
<script lang="ts">
  import { tick, untrack } from 'svelte';
  import {
    Code,
    Database,
    Eye,
    EyeOff,
    Globe,
    Package,
    Server,
    TriangleAlert,
  } from '@lucide/svelte';
  import { ApiError, oidcStartUrl } from '$lib/api/client';
  import { authFailureText, sentence } from '$lib/errors';
  import { router } from '$lib/router';
  import { session } from '$lib/state/session.svelte';
  import { DEVELOPER_NAME, DEVELOPER_URL, REPO_URL, SITE_HOST, SITE_URL } from '$lib/links';
  import GithubMark from '$lib/icons/GithubMark.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import Button from '$lib/components/Button.svelte';
  import Field from '$lib/components/Field.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import Input from '$lib/components/Input.svelte';

  let username = $state('');
  let password = $state('');
  let touched = $state({ username: false, password: false });
  let submitting = $state(false);
  let failure = $state<ApiError | null>(null);
  /**
   * Why a single sign-on attempt was sent back here. The controller cannot
   * render a page of its own for a failed SSO callback, so it redirects to
   * /login with the reason in the query string; it is read once and then
   * dropped from the address, so a reload does not repeat a stale complaint.
   */
  let ssoFailure = $state(
    untrack(() => new URLSearchParams(location.search).get('error')?.trim() ?? ''),
  );
  let revealed = $state(false);
  let capsLock = $state(false);
  let usernameInput = $state<HTMLInputElement | null>(null);
  let passwordInput = $state<HTMLInputElement | null>(null);

  /**
   * Where to go once signed in.
   *
   * Anything other than the overview is a deep link somebody followed while
   * signed out -- GitHub returning an operator to the App setup address with a
   * single-use code in it, most importantly -- and sending them to the overview
   * throws it away.
   */
  const destination = untrack(() =>
    location.pathname === '/login' ? '/' : location.pathname + location.search,
  );

  const meta = $derived(session.meta);

  /**
   * Whether this is the shell's /login route rather than the sign-in screen.
   *
   * The router serves /login to somebody already in the product too -- that is
   * the only way the "no sign-in required" statement is ever seen -- and there
   * the page sits inside the navigation and the footer, which already say what
   * Zoomies is. The brand panel and the full-window layout are for the screen
   * that has nothing else on it.
   */
  const embedded = $derived(session.phase === 'ready');

  /**
   * The address being signed in to, as the browser reached it. Somebody with
   * accounts on a staging and a production controller should not have to read
   * the address bar to know which one they are about to type a password into.
   * The browser's own idea of the host is the one that is always right: the
   * configured external URL can be missing, can be a loopback address reached
   * through a tunnel, or can name an address this browser did not use -- and
   * it is this address the password is about to be sent to.
   */
  const host = location.host;

  /**
   * The rings' radii, as fractions of the square they are drawn in, and the
   * two that carry a highlight: a Runner Blue arc on one and a short tick on
   * the next.
   */
  const ARC_RING = 0.27;
  const TICK_RING = 0.37;
  const RING_RADII = [0.18, ARC_RING, TICK_RING, 0.46];

  /** A dash covering `share` of the circle of this radius, and a gap for the rest. */
  function dash(radius: number, share: number): string {
    const around = 2 * Math.PI * radius;
    return `${around * share} ${around}`;
  }

  /**
   * What the brand panel says Zoomies is. Three facts, each true of every
   * installation, because whoever reads them is usually not the operator: a
   * developer sent this address by the colleague who installed it.
   */
  const facts = [
    {
      icon: Package,
      title: 'A clean runner for every job',
      detail: 'Ephemeral containers, destroyed when the job ends.',
    },
    {
      icon: Database,
      title: 'One binary, one SQLite file',
      detail: 'No Kubernetes and no database server to run.',
    },
    {
      icon: Code,
      title: 'Free and open source',
      detail: 'Read the source before you run it.',
    },
  ];

  $effect(() => {
    if (ssoFailure) router.setQuery({ error: null });
  });

  const usernameError = $derived(
    touched.username && username.trim() === '' ? 'Enter your username.' : undefined,
  );
  const passwordError = $derived(
    touched.password && password === '' ? 'Enter your password.' : undefined,
  );

  /**
   * What actually went wrong, in the operator's terms. A controller that never
   * answered and a password that was refused look identical in a generic
   * "sign-in failed", and they need completely different next steps.
   *
   * A 401 and a 403 are shown in the server's own words. It distinguishes a
   * wrong password from a disabled account and from an account that signs in
   * through SSO, and a 403 on this route is the origin check refusing the
   * request -- a proxy or external_url problem, which "wrong password" would
   * send the operator off to fix in entirely the wrong place.
   */
  const failureText = $derived.by(() => {
    if (!failure) return ssoFailure ? sentence(ssoFailure) : '';
    if (failure.status === 429) {
      return 'Too many sign-in attempts from this address. Wait a minute, then try again.';
    }
    return authFailureText(failure);
  });

  /*
    The cursor starts where there is something to type: a browser that has
    filled the username in should not make the operator tab past it.

    `placed` is a plain variable rather than state on purpose. The effect must
    fire once, when the fields first exist, and never again -- tracking it, or
    reading `username` reactively, would move the cursor out of the field
    somebody is typing in.
  */
  let placed = false;
  $effect(() => {
    if (placed || !usernameInput || meta?.auth_disabled) return;
    placed = true;
    // Do not open a phone keyboard or scroll the form before the user chooses a field.
    if (matchMedia('(max-width: 960px), (pointer: coarse)').matches) return;
    untrack(() => (username.trim() === '' ? usernameInput : passwordInput))?.focus();
  });

  /**
   * Caps lock costs an attempt and, at the rate limit, a minute. The state is
   * only knowable from a key event, so it is read from every one the two
   * fields see and cleared when the password field is left.
   */
  function readCapsLock(event: KeyboardEvent): void {
    if (typeof event.getModifierState !== 'function') return;
    capsLock = event.getModifierState('CapsLock');
  }

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    touched = { username: true, password: true };
    if (username.trim() === '' || password === '') {
      // Derived from the model, not from the DOM: Svelte batches state into a
      // microtask, so a query for `aria-invalid="true"` here matches nothing on
      // the first submit of an empty form -- which is precisely the keyboard
      // user pressing Enter that this line exists for.
      (username.trim() === '' ? usernameInput : passwordInput)?.focus();
      return;
    }
    submitting = true;
    failure = null;
    ssoFailure = '';
    try {
      await session.login(username.trim(), password);
      router.navigate(destination);
    } catch (cause) {
      failure =
        cause instanceof ApiError
          ? cause
          : new ApiError({ status: 0, code: 'internal', message: 'Sign-in failed. Try again.' });
      // The password is cleared, so that is where the cursor belongs: retyping
      // it is the next thing to do whichever failure this was. The field is
      // untouched again with it -- "Enter your password." under a field this
      // page emptied itself is an accusation, and it would sit directly below
      // the banner that already said what went wrong.
      password = '';
      touched = { ...touched, password: false };
      revealed = false;
      // After the flush: the failure box appears above the form and the field
      // is emptied in the same update, and focus set before that lands on an
      // element the render is about to move.
      void tick().then(() => passwordInput?.focus());
    } finally {
      submitting = false;
    }
  }
</script>

{#snippet rings(where: 'panel' | 'band', size: number)}
  <!-- The circular motion path the dog runs in, centred just past the corner
       so only an arc of it shows, and kept away from the lockup: the brand
       pack allows no texture on or behind the artwork itself. Drawn at the
       size it is shown rather than scaled, so every stroke stays a hairline. -->
  {@const c = size / 2}
  <svg
    class="rings {where}"
    style:--rings="{size}px"
    width={size}
    height={size}
    viewBox="0 0 {size} {size}"
    aria-hidden="true"
    focusable="false"
  >
    {#each RING_RADII as radius (radius)}
      <circle cx={c} cy={c} r={radius * size} />
    {/each}
    <circle
      class="arc"
      cx={c}
      cy={c}
      r={ARC_RING * size}
      stroke-dasharray={dash(ARC_RING * size, 0.13)}
      transform="rotate(218 {c} {c})"
    />
    <circle
      class="tick"
      cx={c}
      cy={c}
      r={TICK_RING * size}
      stroke-dasharray={dash(TICK_RING * size, 0.04)}
      transform="rotate(236 {c} {c})"
    />
  </svg>
{/snippet}

<!--
  Four regions, in the order they are read: the lockup, the form, what Zoomies
  is, and which build this is. On a desktop the first and third stack into the
  panel on the left; on a phone they become a band above the form and a row of
  links below it. The source order is the reading order in both, so the three
  outbound links come after the form for a keyboard, not before it.
-->
<div class="signin" class:embedded>
  {#if !embedded}
    <div class="mark">
      {@render rings('band', 200)}
      <span class="lockup"><Logo variant="lockup" size={84} label="Zoomies" /></span>
    </div>
  {/if}

  <div class="form-side">
    <div class="form-column">
      {#if !embedded}
        <p class="instance" title="The Zoomies instance at this address">
          <Server size={12} aria-hidden="true" />
          <span class="sr-only">This instance:</span>
          <span class="host">{host}</span>
        </p>
      {/if}

      {#if meta?.auth_disabled}
        <h1>No sign-in required</h1>
        <p class="lede">
          Authentication is switched off in this instance's configuration, so there is nothing to
          sign in to. Anyone who can reach this address has full access.
        </p>
        <Button variant="primary" size="lg" full href="/">Continue to the dashboard</Button>
        <p class="note">
          Turn authentication back on in the configuration file before this instance is reachable by
          anyone you do not trust.
        </p>
      {:else}
        <h1>Sign in</h1>
        <p class="lede">Manage the runner fleet on this instance.</p>

        {#if failureText}
          <p class="failure" role="alert">
            <TriangleAlert size={15} aria-hidden="true" />
            <span>{failureText}</span>
          </p>
        {/if}

        <form onsubmit={submit} novalidate>
          <Field label="Username" error={usernameError}>
            {#snippet children({ id, describedBy, invalid })}
              <Input
                bind:value={username}
                bind:element={usernameInput}
                {id}
                {describedBy}
                {invalid}
                size="lg"
                name="username"
                autocomplete="username"
                autocapitalize="none"
                spellcheck={false}
                disabled={submitting}
                onkeydown={readCapsLock}
                onblur={() => (touched = { ...touched, username: true })}
              />
            {/snippet}
          </Field>

          <!-- The caps-lock warning goes in `notice`, not `hint`: hint is the
               branch Field drops the moment there is an error, which is exactly
               when caps lock is most likely to be the reason for one.

               The hint holds the row an error takes, so "Enter your password."
               appearing on blur cannot move the button out from under a pointer
               on its way to it. It used to read "the one you chose when this
               instance was set up", which is true of the first administrator
               and of nobody else: every other password was set by an
               administrator, and an administrator is also who can reset it. -->
          <Field
            label="Password"
            hint="Forgotten it? An administrator can reset it."
            error={passwordError}
            notice={capsLock ? 'Caps lock is on.' : undefined}
          >
            {#snippet children({ id, describedBy, invalid })}
              <Input
                bind:value={password}
                bind:element={passwordInput}
                {id}
                {describedBy}
                {invalid}
                size="lg"
                type={revealed ? 'text' : 'password'}
                name="password"
                autocomplete="current-password"
                disabled={submitting}
                onkeydown={readCapsLock}
                onblur={() => {
                  touched = { ...touched, password: true };
                  capsLock = false;
                }}
              >
                {#snippet trailing()}
                  <IconButton
                    icon={revealed ? EyeOff : Eye}
                    label={revealed ? 'Hide password' : 'Show password'}
                    pressed={revealed}
                    disabled={submitting}
                    onclick={() => {
                      revealed = !revealed;
                      passwordInput?.focus();
                    }}
                  />
                {/snippet}
              </Input>
            {/snippet}
          </Field>

          <Button type="submit" variant="primary" size="lg" full loading={submitting}>
            Sign in
          </Button>
        </form>

        {#if meta?.oidc_enabled}
          <div class="divider"><span>or</span></div>
          <Button href={oidcStartUrl()} size="lg" full
            >{meta.oidc_label ?? 'Sign in with SSO'}</Button
          >
        {/if}
      {/if}
    </div>
  </div>

  {#if !embedded}
    <!--
      What Zoomies is, for the person who did not install it -- and until now the
      page named the product and nothing else. Free and open source is the fact
      worth carrying, because it is what makes "you could run your own" a real
      sentence. The links are the ones the site and every signed-in page carry,
      from links.ts, and each leaves the product, so each opens a new tab.
    -->
    <div class="about">
      {@render rings('panel', 840)}
      <div class="pitch">
        <p class="tagline">GitHub Actions runners on machines you own.</p>
        <p class="pitch-lede">
          Zoomies starts a fresh runner for every queued job and tears it down when the job is done.
        </p>
        <ul class="facts">
          {#each facts as fact (fact.title)}
            <li>
              <span class="fact-icon"><fact.icon size={16} aria-hidden="true" /></span>
              <span class="fact-text">
                <strong>{fact.title}</strong>
                <span>{fact.detail}</span>
              </span>
            </li>
          {/each}
        </ul>
      </div>
      <nav class="links" aria-label="About Zoomies">
        <a href={SITE_URL} target="_blank" rel="noopener noreferrer">
          <Globe size={15} aria-hidden="true" />
          {SITE_HOST}<span class="sr-only"> (opens in a new tab)</span>
        </a>
        <a href={REPO_URL} target="_blank" rel="noopener noreferrer">
          <GithubMark size={15} aria-hidden="true" />
          GitHub<span class="sr-only"> (opens in a new tab)</span>
        </a>
        <span class="credit">
          Developed by
          <a href={DEVELOPER_URL} target="_blank" rel="noopener noreferrer">
            {DEVELOPER_NAME}<span class="sr-only"> (opens in a new tab)</span>
          </a>
        </span>
      </nav>
    </div>

    <!-- A build number is about the installation, not about signing in, so it
         sits under the form rather than inside it. -->
    <div class="meta">
      <span class="build">
        <span class="product">Zoomies</span>
        {#if meta?.version}<span class="version">{meta.version}</span>{/if}
      </span>
      {#if !meta?.auth_disabled}
        <span class="no-account">No account? Ask an administrator of this instance to add you.</span
        >
      {/if}
    </div>
  {/if}
</div>

<style>
  /*
    The measures this layout is cut to. The panel takes a share of the window
    between a floor that still holds the lockup beside its padding and a
    ceiling past which the extra width would only be black. The lockup is the
    brand's primary full logo, given the room the brand page asks for on this
    screen and never under its 220px minimum; its artwork carries its own clear
    space, which starts 12.6% in from the left and 14.6% down from the top of
    the file, and the panel pulls the frame out by that much so the dog lines
    up with the text beneath it without any of that clear space being lost.
  */
  .signin {
    --panel-width: clamp(22rem, 42vw, 35rem);
    --panel-pad-x: var(--z-space-16);
    --lockup: 16.25rem;

    display: grid;
    grid-template-columns: var(--panel-width) minmax(0, 1fr);
    grid-template-rows: auto minmax(0, 1fr) auto;
    grid-template-areas:
      'mark form'
      'about form'
      'about meta';
    min-height: 100vh;
    min-height: 100dvh;
    background: var(--z-surface);
  }

  /* ---------------------------------------------------------- brand panel */

  .mark,
  .about {
    position: relative;
    overflow: hidden;
    background: var(--z-panel-bg);
    color: var(--z-panel-text);
    border-right: var(--z-border-width) solid var(--z-border);
  }
  .mark {
    grid-area: mark;
    padding: var(--z-space-12) var(--panel-pad-x) 0;
  }
  .lockup {
    display: block;
    width: var(--lockup);
    margin: calc(var(--lockup) * -0.146) 0 0 calc(var(--lockup) * -0.126);
  }
  .lockup :global(.logo.lockup) {
    justify-content: flex-start;
  }
  .about {
    grid-area: about;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-10);
    padding: var(--z-space-4) var(--panel-pad-x) var(--z-space-10);
  }
  .pitch {
    position: relative;
    max-width: 25rem;
  }
  .tagline {
    margin: 0;
    font-size: var(--z-text-2xl);
    line-height: var(--z-leading-2xl);
    font-weight: var(--z-weight-semibold);
    letter-spacing: var(--z-tracking-tight);
    text-wrap: balance;
  }
  .pitch-lede {
    margin: var(--z-space-3) 0 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-panel-text-muted);
  }
  .facts {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    margin: var(--z-space-8) 0 0;
    padding: 0;
    list-style: none;
  }
  .facts li {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
  }
  .fact-icon {
    display: grid;
    flex: none;
    width: var(--z-space-8);
    height: var(--z-space-8);
    place-items: center;
    border: var(--z-border-width) solid var(--z-panel-border);
    border-radius: var(--z-radius-md);
    background: var(--z-panel-raised);
  }
  .fact-text {
    display: flex;
    flex-direction: column;
    gap: var(--z-nudge-2);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-panel-text-subtle);
  }
  .fact-text strong {
    font-weight: var(--z-weight-semibold);
    color: var(--z-panel-text);
  }

  /*
    The links sit at the foot of the panel, whatever height the window gives
    it. Their colours are local so that the phone layout, where they leave the
    panel for the page, can hand them the theme's own.
  */
  .links {
    --link: var(--z-panel-text-muted);
    --link-hover: var(--z-panel-text);
    --credit: var(--z-panel-text-subtle);
    --credit-link: var(--z-panel-text);

    position: relative;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2) var(--z-space-6);
    margin-top: auto;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    font-weight: var(--z-weight-medium);
  }
  .links a {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    color: var(--link);
    text-decoration: none;
    transition: color var(--z-motion-fast) var(--z-ease);
  }
  .links a:hover,
  .links a:focus-visible {
    color: var(--link-hover);
    text-decoration: underline;
    text-underline-offset: var(--z-underline-offset);
  }
  .credit {
    margin-left: auto;
    font-weight: var(--z-weight-normal);
    color: var(--credit);
  }
  .credit a {
    display: inline;
    font-weight: var(--z-weight-semibold);
    color: var(--credit-link);
  }

  /*
    Decoration, so it takes no pointer and no space, and its strokes stay a
    hairline at whatever size it is drawn.
  */
  .rings {
    position: absolute;
    pointer-events: none;
  }
  .rings circle {
    fill: none;
    stroke: var(--z-panel-ring);
    stroke-width: var(--z-border-width);
  }
  /* 2px is this drawing's own measure, not the thick border: that token
     means selected, focused or wrong, and an arc means none of them. */
  .rings .arc {
    stroke: var(--z-brand-runner-blue);
    stroke-width: 2px;
    stroke-linecap: round;
  }
  .rings .tick {
    stroke: var(--z-panel-text-subtle);
    stroke-linecap: round;
  }
  .rings.panel {
    right: calc(var(--z-space-5) * -1 - var(--rings) / 2);
    bottom: calc(var(--z-space-5) * -1 - var(--rings) / 2);
  }
  .rings.band {
    display: none;
  }

  /* ---------------------------------------------------------------- form */

  .form-side {
    grid-area: form;
    min-width: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: var(--z-space-12) var(--z-space-8) var(--z-space-6);
  }
  .form-column {
    display: flex;
    flex-direction: column;
    width: 100%;
    max-width: 22.5rem;
    min-width: 0;
  }
  .instance {
    display: inline-flex;
    align-self: flex-start;
    align-items: center;
    gap: var(--z-space-2);
    max-width: 100%;
    height: var(--z-space-6);
    margin: 0 0 var(--z-space-5);
    padding: 0 var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-full);
    background: var(--z-bg);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .instance :global(svg) {
    flex: none;
    color: var(--z-text-subtle);
  }
  .host {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  h1 {
    margin: 0;
    font-size: var(--z-text-2xl);
    line-height: var(--z-leading-2xl);
    font-weight: var(--z-weight-semibold);
    letter-spacing: var(--z-tracking-tight);
    color: var(--z-text);
  }
  .lede {
    margin: var(--z-space-1) 0 var(--z-space-6);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
    text-wrap: pretty;
  }
  form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  form :global(.btn) {
    margin-top: var(--z-space-1);
  }
  .failure {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: 0 0 var(--z-space-5);
    padding: var(--z-space-3);
    border: var(--z-border-width) solid var(--z-danger-border);
    border-radius: var(--z-radius-md);
    background: var(--z-danger-subtle);
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
  }
  .failure span {
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .failure :global(svg) {
    flex: none;
    margin-top: var(--z-nudge-3);
    color: var(--z-danger);
  }
  .divider {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    margin: var(--z-space-5) 0;
    color: var(--z-text-subtle);
    font-size: var(--z-text-xs);
  }
  .divider::before,
  .divider::after {
    content: '';
    flex: 1;
    height: var(--z-border-width);
    background: var(--z-border);
  }
  .note {
    margin: var(--z-space-4) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }

  .meta {
    grid-area: meta;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-2) var(--z-space-4);
    padding: var(--z-space-5) var(--z-space-8);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }
  .build {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .product {
    font-weight: var(--z-weight-semibold);
    color: var(--z-text-muted);
  }
  .version {
    overflow: hidden;
    padding: 0 var(--z-space-2);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-full);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-2xs);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /*
    Between the phone and the wide desktop the panel is at its floor, and the
    full inset would leave the tagline a word or two per line.
  */
  @media (max-width: 1180px) {
    .signin {
      --panel-pad-x: var(--z-space-10);
    }
  }

  /*
    A laptop with its browser chrome can leave well under 720px of window. The
    facts are the part of the panel that can go without the page losing its
    meaning, and they go before the page grows a scrollbar.
  */
  @media (min-width: 769px) and (max-height: 720px) {
    .facts {
      display: none;
    }
  }

  /*
    The phone: the panel's two halves come apart. The lockup becomes a band at
    the top, at the brand's 220px minimum, and the form rises over its lower
    edge as a sheet. The pitch is dropped -- a phone is for signing in to a
    fleet somebody already chose -- but the links, which are the page's only
    answer to "what is this", move below the form in the theme's colours.

    The band, the sheet, the links and the build line read one 40px gutter,
    so they share a left edge with clear room between the form and the glass,
    and each side of it yields to a notch or a rounded corner rather than the
    artwork or a field running under one. The fields lose that width, and a
    320px phone still keeps 240px of them.
  */
  @media (max-width: 768px) {
    .signin {
      --lockup: 13.75rem;
      --gutter: var(--z-space-10);

      display: flex;
      flex-direction: column;
    }
    .mark,
    .about {
      border-right: 0;
    }
    /* Installed to a home screen, the page is drawn under the status bar,
       so the band runs up behind it and the lockup starts below it. */
    .mark {
      display: flex;
      justify-content: center;
      padding: calc(var(--z-space-4) + var(--z-safe-top))
        max(var(--gutter), env(safe-area-inset-right)) var(--z-space-8)
        max(var(--gutter), env(safe-area-inset-left));
    }
    .lockup {
      margin: 0;
    }
    .lockup :global(.logo.lockup) {
      justify-content: center;
    }
    .rings.band {
      display: block;
      right: calc(var(--z-space-4) * -1 - var(--rings) / 2);
      bottom: calc(var(--z-space-4) * -1 - var(--rings) / 2);
    }
    .rings.panel,
    .pitch {
      display: none;
    }
    .form-side {
      position: relative;
      z-index: 1;
      margin-top: calc(var(--z-space-3) * -1);
      padding: var(--z-space-6) max(var(--gutter), env(safe-area-inset-right)) var(--z-space-2)
        max(var(--gutter), env(safe-area-inset-left));
      border-radius: var(--z-radius-lg) var(--z-radius-lg) 0 0;
      background: var(--z-surface);
    }
    .form-column {
      max-width: none;
    }
    .about {
      overflow: visible;
      padding: var(--z-space-6) max(var(--gutter), env(safe-area-inset-right)) 0
        max(var(--gutter), env(safe-area-inset-left));
      background: none;
      color: var(--z-text);
    }
    .links {
      --link: var(--z-text-muted);
      --link-hover: var(--z-text);
      --credit: var(--z-text-subtle);
      --credit-link: var(--z-text-muted);

      justify-content: center;
      gap: var(--z-space-2) var(--z-space-5);
    }
    .credit {
      margin-left: 0;
    }
    .meta {
      flex-direction: column;
      justify-content: center;
      padding: var(--z-space-3) max(var(--gutter), env(safe-area-inset-right))
        calc(var(--z-space-4) + var(--z-safe-bottom)) max(var(--gutter), env(safe-area-inset-left));
      text-align: center;
    }
  }

  /*
    A phone on its side is wider than the band layout's breakpoint and keeps
    the desktop split, but it is still worked with a thumb, so the controls
    grow to the touch size at any width a phone can reach. They grow here
    rather than in their components because this is the one form that is the
    whole page: a dense table's inputs cannot afford a 44px row, and the
    sign-in fields have nowhere else to be. The button is allowed to wrap for
    the same reason -- a long label on a narrow phone is better on two lines
    than cut off.
  */
  @media (max-width: 960px) {
    .form-side :global(input) {
      min-height: var(--z-control-touch);
      font-size: var(--z-control-font-touch);
    }
    .form-side :global(.icon-btn) {
      width: var(--z-control-touch);
      height: var(--z-control-touch);
    }
    .form-side :global(.has-trailing input) {
      padding-right: calc(var(--z-control-touch) + var(--z-space-2));
    }
    .form-side :global(.btn) {
      min-height: var(--z-control-touch);
      white-space: normal;
      overflow-wrap: anywhere;
    }
  }

  /*
    Inside the signed-in shell, which already carries the brand, the version
    and the links: the form alone, where the page's content starts. These are
    more specific than every rule above, the phone's included, so they hold at
    any width.
  */
  .signin.embedded {
    display: block;
    min-height: 0;
    background: none;
  }
  .embedded .form-side {
    display: block;
    margin: 0;
    padding: 0;
    border-radius: 0;
    background: none;
  }
</style>

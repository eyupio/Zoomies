<!--
  The runner log viewer.

  The hard requirement is 100k lines without jank, and one decision carries most
  of it: log frames arrive from the relay far faster than the screen refreshes,
  so they are queued and written to the terminal **once per animation frame**
  rather than once per frame received. A runner emitting a thousand lines a
  second then costs sixty writes a second, not a thousand. That batch is the
  difference between a viewer that scrolls smoothly and one that locks the tab;
  everything else here is ordinary.

  The rest of the budget is spent the same way:

    * xterm and its addons are imported dynamically, inside `onMount`, so they
      live in this route's chunk and never reach the app shell;
    * the WebGL renderer draws the cells, and falls back to the DOM renderer if
      the context is lost;
    * `fit` is debounced and only runs when the proposed size actually changed;
    * search goes through the search addon, not a scan of the DOM.

  Follow is a mode, not a scroll position. Scrolling away turns it off and a
  floating button turns it back on, so the viewer never yanks itself out from
  under somebody who is reading.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import {
    ArrowDown,
    ChevronDown,
    ChevronUp,
    Download,
    Eraser,
    Search,
    Unplug,
  } from '@lucide/svelte';
  import type { ITheme, Terminal } from '@xterm/xterm';
  import type { FitAddon } from '@xterm/addon-fit';
  import type { ISearchOptions, SearchAddon } from '@xterm/addon-search';
  import type { WebglAddon } from '@xterm/addon-webgl';
  import { runnerLogsDownloadUrl, runnerLogsUrl } from '$lib/api/client';
  import { formatNumber, pluralise } from '$lib/format';
  import { registerSearch } from '$lib/keys';
  import { theme } from '$lib/state/theme.svelte';
  import Button from '$lib/components/Button.svelte';
  import IconButton from '$lib/components/IconButton.svelte';
  import Input from '$lib/components/Input.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Switch from '$lib/components/Switch.svelte';

  interface Props {
    /** The runner whose output to tail. */
    runnerId: string;
    /** Used in the accessible name and in the waiting-for-output line. */
    runnerName?: string;
    class?: string;
  }

  let { runnerId, runnerName, class: className = '' }: Props = $props();

  /** The scrollback the guidelines fix, and the cap on our own line mirror. */
  const SCROLLBACK = 100000;
  /** Lines of backlog to ask the relay for before it starts following. */
  const BACKLOG_LINES = 1000;
  /** Resizing is noisy while a pane is dragged; let it settle before fitting. */
  const FIT_DEBOUNCE_MS = 120;
  /** Long enough that typing does not run a search on every keystroke. */
  const SEARCH_DEBOUNCE_MS = 150;

  type Status = 'starting' | 'live' | 'reconnecting' | 'ended' | 'failed';

  /* -- reactive surface ---------------------------------------------------- */

  let ready = $state(false);
  let bootError = $state('');
  let status = $state<Status>('starting');
  let endedReason = $state('');
  let follow = $state(true);
  let wrap = $state(true);
  let lineCount = $state(0);
  let missedLines = $state(0);
  let cleared = $state(false);
  let query = $state('');
  let matchIndex = $state(-1);
  let matchCount = $state(0);
  let announcement = $state('');
  let host = $state<HTMLDivElement | null>(null);
  let probe = $state<HTMLSpanElement | null>(null);
  let searchField = $state<HTMLInputElement | null>(null);

  const uid = $props.id();
  const noteId = `${uid}-note`;
  const countId = `${uid}-matches`;

  /* -- plain, non-reactive machinery ---------------------------------------
   * None of this is read while rendering, so keeping it out of `$state` keeps
   * a proxy off the hot path.
   * --------------------------------------------------------------------- */

  let term: Terminal | null = null;
  let fit: FitAddon | null = null;
  let finder: SearchAddon | null = null;
  let webgl: WebglAddon | null = null;
  let source: EventSource | null = null;
  let observer: ResizeObserver | null = null;
  let fitTimer: ReturnType<typeof setTimeout> | null = null;
  let frame: number | null = null;
  let queue: string[] = [];
  let opens = 0;
  let disposed = false;

  /**
   * Our own copy of the lines, bounded to the scrollback.
   *
   * It buys two things the terminal cannot give back: an exact count of what
   * arrived, and a wrap toggle that re-renders what is already on screen
   * without losing the tail of a long line. Reading the buffer back out of
   * xterm would only return what was drawn, which with wrapping off has been
   * cut at the right edge.
   */
  let mirror: string[] = [];
  /** The last line so far, still waiting for its newline. */
  let partial = '';

  /* -- theme ---------------------------------------------------------------
   * The palette comes from the design tokens rather than a hard-coded terminal
   * theme, so the log matches the product in both themes. Custom properties
   * cannot be handed to xterm directly -- it parses `#rgb` and `rgb()` and
   * nothing else -- so each one is resolved through a hidden probe element,
   * which also turns the `color-mix` below into plain rgb().
   * --------------------------------------------------------------------- */

  const ANSI = {
    // Black and white are the two that cannot be mapped literally: a real black
    // is invisible on the dark theme's near-black well. Both become greys that
    // clear AA on either background.
    black: '--z-text-subtle',
    red: '--z-danger',
    green: '--z-idle',
    yellow: '--z-pending',
    blue: '--z-accent',
    magenta: '--log-magenta',
    cyan: '--z-busy',
    white: '--z-text-muted',
  } as const;

  function resolve(token: string): string {
    if (!probe) return 'rgb(0, 0, 0)';
    probe.style.color = `var(${token})`;
    return getComputedStyle(probe).color;
  }

  /** xterm's decoration options insist on `#rrggbb`, so rgb() is converted. */
  function hex(colour: string): string {
    const parts = colour.match(/\d+/g);
    if (!parts || parts.length < 3) return '#000000';
    return `#${parts
      .slice(0, 3)
      .map((n) => Number(n).toString(16).padStart(2, '0'))
      .join('')}`;
  }

  function palette(): ITheme {
    const foreground = resolve('--z-text');
    const muted = resolve('--z-text-muted');
    const next: Record<string, string> = {
      background: resolve('--z-surface-sunken'),
      foreground,
      cursor: resolve('--z-accent'),
      cursorAccent: resolve('--z-surface-sunken'),
      selectionBackground: resolve('--z-accent-subtle'),
      selectionForeground: foreground,
      selectionInactiveBackground: resolve('--z-neutral-subtle'),
    };
    for (const [name, token] of Object.entries(ANSI)) {
      const colour = resolve(token);
      // The bright variants reuse the same hue deliberately: every ratio in the
      // palette has been measured, and inventing lighter versions of them here
      // would quietly undo that.
      next[name] = colour;
      next[`bright${name[0]?.toUpperCase() ?? ''}${name.slice(1)}`] = colour;
    }
    next.brightBlack = muted;
    next.brightWhite = foreground;
    return next as ITheme;
  }

  function searchOptions(): ISearchOptions {
    return {
      decorations: {
        matchBackground: hex(resolve('--z-accent-subtle')),
        matchBorder: hex(resolve('--z-accent-border')),
        matchOverviewRuler: hex(resolve('--z-accent')),
        activeMatchBackground: hex(resolve('--z-accent-border')),
        activeMatchBorder: hex(resolve('--z-accent')),
        activeMatchColorOverviewRuler: hex(resolve('--z-pending')),
      },
    };
  }

  /* -- writing -------------------------------------------------------------- */

  /** Queue a chunk. One animation frame later it is written with its neighbours. */
  function enqueue(text: string): void {
    queue.push(text);
    if (frame !== null) return;
    frame = requestAnimationFrame(flush);
  }

  function flush(): void {
    frame = null;
    if (queue.length === 0) return;
    const batch = queue.join('');
    queue = [];
    if (!term) return;
    term.write(batch);
    record(batch);
    if (follow) term.scrollToBottom();
  }

  /** Keep the mirror and the counters in step with what was just written. */
  function record(text: string): void {
    const pieces = (partial + text).split('\n');
    partial = pieces.pop() ?? '';
    if (pieces.length === 0) return;
    cleared = false;
    for (const piece of pieces) mirror.push(piece.replace(/\r$/, ''));
    if (mirror.length > SCROLLBACK) mirror.splice(0, mirror.length - SCROLLBACK);
    lineCount += pieces.length;
    if (!follow) missedLines += pieces.length;
  }

  /** Everything the mirror holds, ready to be written into a fresh screen. */
  function replay(): string {
    return mirror.join('\n') + (mirror.length > 0 ? '\n' : '') + partial;
  }

  /** DECAWM. With it off a long line stops at the right edge instead of wrapping. */
  function wrapMode(on: boolean): string {
    return on ? '\x1b[?7h' : '\x1b[?7l';
  }

  /** Hide the cursor -- this is somebody else's output, not a prompt -- and set wrapping. */
  function prepare(target: Terminal): void {
    target.write(`\x1b[?25l${wrapMode(wrap)}`);
  }

  function setWrap(on: boolean): void {
    wrap = on;
    if (!term) return;
    const text = replay();
    term.reset();
    prepare(term);
    if (text !== '') term.write(text);
    if (follow) term.scrollToBottom();
    announcement = on ? 'Long lines wrap.' : 'Long lines are cut off at the right edge.';
  }

  function clearScreen(): void {
    mirror = [];
    partial = '';
    lineCount = 0;
    missedLines = 0;
    cleared = true;
    if (term) {
      term.reset();
      prepare(term);
    }
    announcement = 'Cleared what was on screen. Nothing on the host was deleted.';
  }

  function setFollow(on: boolean): void {
    follow = on;
    if (!on) return;
    missedLines = 0;
    term?.scrollToBottom();
  }

  /* -- the stream ----------------------------------------------------------- */

  function connect(): void {
    source?.close();
    const stream = new EventSource(runnerLogsUrl(runnerId, { tail: BACKLOG_LINES, follow: true }));
    source = stream;

    stream.addEventListener('open', () => {
      opens += 1;
      status = 'live';
      // A reconnect replays the backlog, so mark the seam rather than letting
      // the same hundred lines appear twice with no explanation.
      if (opens > 1) enqueue('\r\n\x1b[2m-- reconnected to the log stream --\x1b[0m\r\n');
      else announcement = 'Attached to the log stream.';
    });

    stream.addEventListener('log', (event: Event) => {
      try {
        const chunk: unknown = JSON.parse((event as MessageEvent<string>).data);
        if (typeof chunk === 'string') enqueue(chunk);
      } catch {
        // A frame we cannot parse is one chunk of output lost, not a reason to
        // tear the viewer down.
      }
    });

    stream.addEventListener('end', (event: Event) => {
      let reason = "the runner's output ended";
      try {
        const payload = JSON.parse((event as MessageEvent<string>).data) as { reason?: string };
        if (payload.reason) reason = payload.reason;
      } catch {
        /* keep the default wording */
      }
      endedReason = reason;
      status = 'ended';
      announcement = `The log stream ended: ${reason}.`;
      stream.close();
    });

    stream.onerror = () => {
      if (stream.readyState === EventSource.CLOSED) {
        status = 'failed';
        announcement = 'The log stream stopped.';
      } else {
        status = 'reconnecting';
      }
    };
  }

  function reconnect(): void {
    status = 'starting';
    endedReason = '';
    connect();
  }

  /* -- fitting -------------------------------------------------------------- */

  function scheduleFit(): void {
    if (fitTimer !== null) clearTimeout(fitTimer);
    fitTimer = setTimeout(() => {
      fitTimer = null;
      if (!term || !fit) return;
      const proposed = fit.proposeDimensions();
      // Only on a real size change: re-laying out 100k lines because a scrollbar
      // appeared is exactly the jank this viewer exists to avoid.
      if (!proposed || proposed.cols < 1 || proposed.rows < 1) return;
      if (proposed.cols === term.cols && proposed.rows === term.rows) return;
      fit.fit();
      if (follow) term.scrollToBottom();
    }, FIT_DEBOUNCE_MS);
  }

  /* -- search ---------------------------------------------------------------- */

  function find(needle: string, direction: 'next' | 'previous', incremental = false): void {
    if (!finder) return;
    if (needle === '') {
      finder.clearDecorations();
      matchIndex = -1;
      matchCount = 0;
      return;
    }
    const options: ISearchOptions = { ...searchOptions(), incremental };
    if (direction === 'next') finder.findNext(needle, options);
    else finder.findPrevious(needle, options);
  }

  const hasQuery = $derived(query.trim() !== '');

  const matchSummary = $derived.by(() => {
    if (!hasQuery) return '';
    if (matchCount === 0) return 'No matches';
    if (matchIndex < 0) return `${formatNumber(matchCount)} matches`;
    return `${formatNumber(matchIndex + 1)} of ${formatNumber(matchCount)}`;
  });

  const statusLabel = $derived(
    status === 'live'
      ? 'Streaming'
      : status === 'starting'
        ? 'Connecting'
        : status === 'reconnecting'
          ? 'Reconnecting'
          : status === 'ended'
            ? 'Ended'
            : 'Stopped',
  );

  /* -- lifecycle -------------------------------------------------------------- */

  onMount(() => {
    void start();
    return () => {
      disposed = true;
      if (frame !== null) cancelAnimationFrame(frame);
      if (fitTimer !== null) clearTimeout(fitTimer);
      host?.removeEventListener('keydown', onTerminalKeydown);
      observer?.disconnect();
      source?.close();
      webgl?.dispose();
      term?.dispose();
      term = null;
    };
  });

  async function start(): Promise<void> {
    const element = host;
    if (!element) return;
    try {
      const [{ Terminal: XTerm }, { FitAddon: Fit }, { SearchAddon: Finder }] = await Promise.all([
        import('@xterm/xterm'),
        import('@xterm/addon-fit'),
        import('@xterm/addon-search'),
        // The stylesheet is part of the terminal; importing it here keeps it out
        // of the shell's CSS too.
        import('@xterm/xterm/css/xterm.css'),
      ]);
      if (disposed) return;

      // The host element carries the type tokens, so the terminal's metrics come
      // from the design system rather than from numbers typed in here.
      const style = getComputedStyle(element);
      const fontSize = Number.parseFloat(style.fontSize);
      const leading = Number.parseFloat(style.lineHeight);
      const instance = new XTerm({
        scrollback: SCROLLBACK,
        // Relayed output uses bare newlines; without this every line stair-steps.
        convertEol: true,
        disableStdin: true,
        cursorBlink: false,
        cursorInactiveStyle: 'none',
        fontFamily: style.fontFamily,
        fontSize,
        lineHeight: Number.isFinite(leading) && fontSize > 0 ? leading / fontSize : 1.5,
        // Guarantees AA for whatever colours a workflow decides to print.
        minimumContrastRatio: 4.5,
        theme: palette(),
      });
      const fitAddon = new Fit();
      const searchAddon = new Finder();
      instance.loadAddon(fitAddon);
      instance.loadAddon(searchAddon);
      instance.open(element);
      fitAddon.fit();

      term = instance;
      fit = fitAddon;
      finder = searchAddon;
      prepare(instance);

      searchAddon.onDidChangeResults((event) => {
        matchIndex = event.resultIndex;
        matchCount = event.resultCount;
      });

      instance.onScroll(() => {
        const buffer = instance.buffer.active;
        // Leaving the bottom is what turns follow off -- including when a search
        // jumps to a match, which is precisely when it should stop moving.
        if (follow && buffer.viewportY < buffer.baseY) follow = false;
      });

      // WebGL does the drawing. If the context goes away -- a GPU reset, a tab
      // restored from the background -- the addon is disposed and xterm falls
      // back to the DOM renderer on its own.
      try {
        const { WebglAddon: Webgl } = await import('@xterm/addon-webgl');
        if (disposed) return;
        const renderer = new Webgl();
        renderer.onContextLoss(() => {
          renderer.dispose();
          webgl = null;
        });
        instance.loadAddon(renderer);
        webgl = renderer;
      } catch {
        // No WebGL on this machine. The DOM renderer is slower but correct.
      }

      element.addEventListener('keydown', onTerminalKeydown);
      observer = new ResizeObserver(() => scheduleFit());
      observer.observe(element);

      ready = true;
      connect();
    } catch {
      bootError = 'The log viewer could not be loaded. Check your connection, then try again.';
    }
  }

  /** Paging keys scroll the log; with no stdin, xterm would otherwise ignore them. */
  function onTerminalKeydown(event: KeyboardEvent): void {
    if (!term) return;
    switch (event.key) {
      case 'PageUp':
        term.scrollPages(-1);
        break;
      case 'PageDown':
        term.scrollPages(1);
        break;
      case 'Home':
        term.scrollToTop();
        break;
      case 'End':
        term.scrollToBottom();
        break;
      default:
        return;
    }
    event.preventDefault();
  }

  // Re-read the palette whenever the theme changes, including a system change
  // with no click behind it. Reading `theme.resolved` is the subscription.
  $effect(() => {
    void theme.resolved;
    if (!ready || !term) return;
    term.options.theme = palette();
  });

  // `/` focuses the log search on this page.
  $effect(() => registerSearch(searchField));

  // Typing searches, but not on every keystroke.
  $effect(() => {
    const needle = query.trim();
    if (!ready) return;
    const timer = setTimeout(() => find(needle, 'next', true), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  });

  function onSearchKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Enter') return;
    event.preventDefault();
    find(query.trim(), event.shiftKey ? 'previous' : 'next');
  }
</script>

<div class="viewer {className}">
  <div class="toolbar">
    <div class="meter">
      <span class="status" data-status={status}>
        <span class="pip" aria-hidden="true"></span>
        {statusLabel}
      </span>
      <span
        class="lines tabular"
        title="Lines received since this view opened. The scrollback keeps the most recent {formatNumber(
          SCROLLBACK,
        )}."
      >
        {pluralise(lineCount, 'line')}
      </span>
    </div>

    <div class="find">
      <div class="field">
        <Input
          bind:element={searchField}
          bind:value={query}
          type="search"
          size="sm"
          icon={Search}
          placeholder="Search the log"
          ariaLabel="Search the log"
          describedBy={countId}
          onkeydown={onSearchKeydown}
        />
      </div>
      <span class="matches tabular" id={countId}>{matchSummary}</span>
      <IconButton
        icon={ChevronUp}
        label="Previous match"
        size="sm"
        disabled={!hasQuery}
        onclick={() => find(query.trim(), 'previous')}
      />
      <IconButton
        icon={ChevronDown}
        label="Next match"
        size="sm"
        disabled={!hasQuery}
        onclick={() => find(query.trim(), 'next')}
      />
    </div>

    <div class="modes">
      <div class="mode">
        <Switch bind:checked={follow} label="Follow" onchange={setFollow} />
      </div>
      <div class="mode">
        <Switch bind:checked={wrap} label="Wrap" onchange={setWrap} />
      </div>
      <Button
        size="sm"
        variant="ghost"
        icon={Eraser}
        title="Clear what is on screen. The log on the host is untouched."
        onclick={clearScreen}
      >
        Clear
      </Button>
      <Button
        size="sm"
        variant="secondary"
        icon={Download}
        href={runnerLogsDownloadUrl(runnerId)}
        title="Download the whole log as a text file"
      >
        Download
      </Button>
    </div>
  </div>

  <div class="stage">
    <div
      class="screen"
      bind:this={host}
      role="group"
      aria-label="Log output for {runnerName ?? 'this runner'}"
      aria-describedby={noteId}
    ></div>
    <span class="probe" bind:this={probe} aria-hidden="true"></span>

    {#if bootError}
      <p class="overlay message">{bootError}</p>
    {:else if !ready}
      <div class="overlay">
        <Skeleton height="0.9rem" lines={6} />
      </div>
    {:else if lineCount === 0 && status !== 'failed'}
      <p class="overlay message">
        {cleared
          ? 'Cleared. Anything the runner writes from now on appears here.'
          : status === 'ended'
            ? 'This runner produced no output.'
            : 'Waiting for output. Nothing has been written yet.'}
      </p>
    {/if}

    {#if !follow && ready}
      <button type="button" class="jump" onclick={() => setFollow(true)}>
        <ArrowDown size={13} aria-hidden="true" />
        Jump to latest
        {#if missedLines > 0}
          <span class="jump-count tabular">{pluralise(missedLines, 'new line')}</span>
        {/if}
      </button>
    {/if}
  </div>

  {#if status === 'failed' || status === 'ended'}
    <p class="footnote" class:failed={status === 'failed'}>
      {#if status === 'failed'}
        <Unplug size={13} aria-hidden="true" />
        <span>The log stream stopped. The runner or its host may have gone away.</span>
        <Button size="sm" variant="secondary" onclick={reconnect}>Reconnect</Button>
      {:else}
        <span>The stream ended: {endedReason}. What is on screen is all there was.</span>
      {/if}
    </p>
  {/if}

  <p class="sr-only" id={noteId}>
    Live log output is not announced as it arrives, because a screen reader cannot keep up with a
    running job. Use the download button to read the whole log as a text file instead.
  </p>
  <p class="sr-only" aria-live="polite">{announcement}</p>
</div>

<style>
  .viewer {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    height: 100%;
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    overflow: hidden;
    /* The one colour the token file has no name for: ANSI magenta, mixed from
       two that it does. It is read through the probe, never rendered directly. */
    --log-magenta: color-mix(in srgb, var(--z-danger) 55%, var(--z-accent));
  }
  .toolbar {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-3);
    padding: var(--z-space-2) var(--z-space-3);
    border-bottom: 1px solid var(--z-border);
  }
  .meter {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .status {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    font-weight: var(--z-weight-medium);
  }
  .pip {
    width: var(--z-space-2);
    height: var(--z-space-2);
    border-radius: var(--z-radius-full);
    background: var(--z-neutral);
  }
  .status[data-status='live'] {
    color: var(--z-busy);
  }
  .status[data-status='live'] .pip {
    background: var(--z-busy);
  }
  .status[data-status='starting'],
  .status[data-status='reconnecting'] {
    color: var(--z-pending);
  }
  .status[data-status='starting'] .pip,
  .status[data-status='reconnecting'] .pip {
    background: var(--z-pending);
  }
  .status[data-status='failed'] {
    color: var(--z-danger);
  }
  .status[data-status='failed'] .pip {
    background: var(--z-danger);
  }
  .lines {
    color: var(--z-text-subtle);
  }
  .find {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    min-width: 0;
    flex: 1 1 18rem;
  }
  .field {
    flex: 1 1 auto;
    min-width: 0;
  }
  .matches {
    min-width: 7ch;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
    white-space: nowrap;
  }
  .modes {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    margin-left: auto;
  }
  .mode {
    font-size: var(--z-text-xs);
  }
  .stage {
    position: relative;
    flex: 1;
    min-height: 22rem;
    background: var(--z-surface-sunken);
  }
  .screen {
    position: absolute;
    inset: 0;
    padding: var(--z-space-2);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .probe {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    pointer-events: none;
  }
  .overlay {
    position: absolute;
    inset: var(--z-space-4);
    pointer-events: none;
  }
  .message {
    margin: 0;
    font-size: var(--z-text-base);
    color: var(--z-text-subtle);
  }
  .jump {
    position: absolute;
    left: 50%;
    bottom: var(--z-space-4);
    transform: translateX(-50%);
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    height: var(--z-space-8);
    padding: 0 var(--z-space-4);
    border: 1px solid var(--z-accent-border);
    border-radius: var(--z-radius-full);
    background: var(--z-accent-subtle);
    color: var(--z-accent);
    font-family: inherit;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    box-shadow: var(--z-shadow-md);
    cursor: pointer;
  }
  .jump:hover {
    background: var(--z-surface-raised);
  }
  .jump-count {
    color: var(--z-text-muted);
  }
  .footnote {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    padding: var(--z-space-3);
    border-top: 1px solid var(--z-border);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .footnote.failed {
    color: var(--z-danger);
  }
  @media (max-width: 768px) {
    .modes {
      margin-left: 0;
    }
  }
</style>

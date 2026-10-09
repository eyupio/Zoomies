/**
 * The filter state the Jobs and Workflows pages share, held in the URL.
 *
 * The two pages ask the same questions of two levels of the same thing -- a
 * run, and the jobs inside it -- and take the same keys, so one address bar
 * serves both: a link into Jobs carrying `?failed=true` means the same on
 * Workflows, and the views row reads the same either way. The keys are the
 * API's own, so the address bar and the request agree and a pasted link
 * reproduces exactly what the sender was looking at.
 */
import {
  JOB_STATES,
  PROVISIONING_STATUSES,
  WAITING_PROVISIONING,
  type JobState,
  type ProvisioningStatus,
} from '../api/types';
import { router } from '../router';
import type { JobFilterState } from './JobFilters.svelte';
import { currentJobView, DEFAULT_JOB_STATE, JOB_VIEWS, type JobView } from './views';

/** Every key a filter lives under, so "has this page been asked anything?" has one answer. */
const FILTER_KEYS = [
  'q',
  'repo',
  'workflow',
  'pool_id',
  'host_id',
  'label',
  'conclusion',
  'state',
  'provisioning',
  'since',
  'until',
  'unmatched',
  'hosted',
  'failed',
  'faulted',
  'all',
] as const;

/** The status keys a view owns. A patch touching one of them is choosing a view. */
const STATUS_KEYS = [
  'state',
  'conclusion',
  'failed',
  'faulted',
  'unmatched',
  'provisioning',
] as const;

export interface JobFilterHandle {
  /** Nobody has asked the page anything: it answers "what is running?". */
  readonly defaulted: boolean;
  readonly filters: JobFilterState;
  /** The status view in force, or `''` when the filters are something no button says. */
  readonly view: JobView | '';
  /** Whether the view is one of the two that describe work still in hand. */
  readonly inHand: boolean;
  /**
   * A search or facet is doing some of the narrowing. The dates are left out on
   * purpose: an empty window has its own advice, to widen it.
   */
  readonly narrowed: boolean;
  patch(next: Partial<JobFilterState>): void;
  clearFilters(): void;
  setView(next: JobView): void;
}

export function jobFilterState(): JobFilterHandle {
  /**
   * Nobody has asked this page anything yet, so it answers the question it is
   * opened with: what is running.
   *
   * Scoped to a URL with no filter at all, not to an absent `state`, because
   * seven of the ten links into these pages carry a filter and no state -- the
   * problems panel's unmatched link, the Overview's failed and outcome links,
   * a date range off the activity matrix. Each of those said what it wanted,
   * and narrowing it to running would answer a different question than the one
   * the sender asked. The address bar is left alone the way Queue's
   * provisioning statuses and Settings' tab are: an absent key means the
   * default, and pressing "All" writes every state out in full so that the two
   * stay distinguishable.
   */
  const defaulted = $derived.by(() => {
    const query = router.query;
    return !FILTER_KEYS.some((key) => query.has(key));
  });

  /**
   * Which of the queue's provisioning statuses to show.
   *
   * Validated rather than asserted, for the reason `state` is: the address bar
   * is whatever somebody pasted, and a cast would send the typo to the server
   * as a filter matching nothing.
   *
   * The default is the interesting part. A view narrowed to queued work means
   * "what is waiting for a runner here", and a job an operator removed from
   * the queue is not -- it stopped counting towards the queue depth on the
   * Overview and towards the Jobs page's own "Queued now" figure, so a list
   * that still carried it disagreed with the number directly above it. Every
   * other view narrows nothing: a removed job belongs in the history the page
   * is, badged as removed, and `All` has to show it. The filter is written
   * into the chips either way, so a shorter list always says why it is
   * shorter.
   */
  const provisioning = $derived.by<ProvisioningStatus[]>(() => {
    const asked = router
      .paramList('provisioning')
      .filter((v): v is ProvisioningStatus =>
        (PROVISIONING_STATUSES as readonly string[]).includes(v),
      );
    if (asked.length > 0) return asked;
    const states = defaulted ? [...DEFAULT_JOB_STATE] : router.paramList('state');
    return states.length === 1 && states[0] === 'queued' ? [...WAITING_PROVISIONING] : [];
  });

  const filters = $derived<JobFilterState>({
    q: router.param('q'),
    repo: router.paramList('repo'),
    workflow: router.paramList('workflow'),
    pool_id: router.paramList('pool_id'),
    host_id: router.paramList('host_id'),
    label: router.paramList('label'),
    conclusion: router.paramList('conclusion'),
    // Validated rather than asserted: `?state=` is whatever was in the address
    // bar, and a cast sends the typo straight to the server as a filter that
    // matches nothing, so the page comes back empty with no explanation.
    state: defaulted
      ? [...DEFAULT_JOB_STATE]
      : router
          .paramList('state')
          .filter((value): value is JobState => (JOB_STATES as readonly string[]).includes(value)),
    provisioning,
    since: router.param('since'),
    until: router.param('until'),
    unmatched: router.param('unmatched') === 'true',
    // Anything but `false` is no filter: the API refuses it with a 400, and a typo in an
    // address is not worth an error page.
    hosted: router.param('hosted') === 'false' ? 'false' : '',
    failed: router.param('failed') === 'true',
    faulted: router.param('faulted') === 'true',
    all: router.param('all') === 'true',
  });

  const view = $derived(currentJobView(filters));

  /**
   * A job whose workflow run has been cancelled is neither waiting nor
   * running: the queued half raises no demand and the running half has had its
   * runner taken away. GitHub's completion delivery decides the conclusion and
   * can be minutes behind, and for that whole window the two views listed work
   * nobody was going to do -- beside the tiles above them, which had already
   * stopped counting it. Every other view is history and shows it, badged.
   */
  const inHand = $derived(view === 'queued' || view === 'running');

  /**
   * Whether an empty grid is the search's doing. The empty states speak for
   * the status view and the switches, and a search that matched nothing under
   * "running" used to be told "nothing is running right now" -- false while
   * other jobs ran -- or "no jobs have run on this fleet", with a switch that
   * cannot fix a search.
   */
  const narrowed = $derived(
    Boolean(filters.q) ||
      filters.repo.length > 0 ||
      filters.workflow.length > 0 ||
      filters.pool_id.length > 0 ||
      filters.host_id.length > 0 ||
      filters.label.length > 0 ||
      filters.conclusion.length > 0,
  );

  /**
   * Merge a filter change into the URL.
   *
   * Only the keys the caller actually passed are written: `setQuery` removes a
   * key whose value is undefined, so spreading a partial object would quietly
   * clear every filter it did not mention.
   */
  function patch(next: Partial<JobFilterState>): void {
    const out: Record<string, string | readonly string[] | null> = { offset: null };
    const choosingStatus = STATUS_KEYS.some((key) => next[key] !== undefined);

    /*
     * Narrowing something else never moves the status. The status in force is
     * written down as part of the same change, because it is only ever implied
     * by what is absent -- the default when nothing is asked, and "every
     * status" when a link asked for something narrower and said nothing about
     * status. Without this, one keystroke in the search box took the page from
     * "running" to every job the fleet has ever run, and removing the last chip
     * from a link that asked for every status took it the other way, down to
     * running. Both are the page answering a question nobody asked.
     */
    if (!choosingStatus) {
      out.state = filters.state.length > 0 ? filters.state : [...JOB_STATES];
    }

    /*
     * A view owns the queue statuses along with the rest of the status keys.
     * Pressing All after looking at what was removed from the queue has to
     * show everything, rather than keeping the narrower filter the last view
     * left behind; clearing the key lets the new view's own default apply.
     * Narrowing something else leaves it alone, because the operator was
     * still looking at that.
     */
    if (choosingStatus && next.provisioning === undefined) out.provisioning = null;

    /*
     * The two switches own the status they contradict, the way a view button
     * does. `unmatched` is queued-and-unclaimed on the server, so a state
     * beside it can only subtract -- and from the default view, where the
     * status has just been written down, `state=in_progress&unmatched=true`
     * matched nothing at all and left the operator on an empty page with no
     * sign of why. "Failed" is the server's own reckoning of a job that went
     * wrong, which is not a state either.
     */
    if (next.unmatched === true) {
      out.state = [];
      out.conclusion = [];
    }
    if (next.failed === true) out.state = [];
    if (next.faulted === true) out.state = [];

    for (const [key, value] of Object.entries(next)) {
      if (typeof value === 'boolean') out[key] = value ? 'true' : null;
      else if (Array.isArray(value)) out[key] = value;
      else out[key] = (value as string) || null;
    }
    router.setQuery(out);
  }

  /**
   * Clearing every filter is a return to the default view, not to everything:
   * a page with nothing asked of it is the page an operator arrives at, and
   * "All" is one button away and says so.
   */
  function clearFilters(): void {
    router.setQuery({ ...Object.fromEntries(FILTER_KEYS.map((k) => [k, null])), offset: null });
  }

  function setView(next: JobView): void {
    const chosen = JOB_VIEWS.find((v) => v.id === next);
    if (chosen) patch(chosen.filters);
  }

  return {
    get defaulted() {
      return defaulted;
    },
    get filters() {
      return filters;
    },
    get view() {
      return view;
    },
    get inHand() {
      return inHand;
    },
    get narrowed() {
      return narrowed;
    },
    patch,
    clearFilters,
    setView,
  };
}

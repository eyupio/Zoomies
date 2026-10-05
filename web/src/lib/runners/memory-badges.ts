/**
 * What a runner's memory is doing, and which of its folders are in memory, as the
 * small badges the Runners page shows and the cards they open.
 *
 * Pure, and imported by a unit test. The grid, the runner's page and the job's
 * drawer all say the same thing about the same runner, so they share the model
 * -- which badge, what it says, what figures it carries -- and differ only in how
 * much room they have to draw it.
 *
 * Two things are said, and they are different kinds of thing. What the memory
 * valve has done is *live*: a runner is lent memory, may be given swap, or hits
 * the most it may hold, and the badge appears and changes as the job runs. Which
 * folders were kept in memory is a *fact about how the runner was made*, decided
 * once when it was created, so those badges are the same for every runner of a
 * pool and say what that runner was given of each, which is not always what the
 * pool asked for.
 *
 * Nothing here is drawn from a colour: a badge names a tone and the component
 * resolves it from the design tokens, because a colour written by hand works in
 * one theme only.
 */
import type { MemoryResourceState, RunnerScratch, ScratchFolder } from '../api/types';
import { formatMegabytes } from '../format';

/** The tones a badge may take. Neutral is a fact; accent is something to notice; pending is something to look at. */
export type BadgeTone = 'accent' | 'neutral' | 'pending' | 'danger';

/** Which glyph a badge or a folder row draws. The component maps these to icons. */
export type BadgeIcon =
  'memory' | 'swap' | 'eye' | 'ceiling' | 'alert' | 'folder' | 'tmp' | 'image' | 'disk';

export type BadgeKey = 'watching' | 'observing' | 'memory' | 'swap' | 'capped' | 'folders';

/** One labelled figure on a card. */
export interface BadgeFigure {
  label: string;
  value: string;
}

/**
 * The runner's memory laid out as one bar: what it was created with, what it was
 * lent, the swap it may use past that, and the most it may hold. All in
 * megabytes, which is what the API reports; the component turns them into
 * widths.
 */
export interface BadgeBar {
  guaranteed: number;
  lent: number;
  swap: number;
  ceiling: number;
}

/** One folder as a row of a card. */
export interface FolderRow {
  kind: ScratchFolder['kind'];
  icon: BadgeIcon;
  label: string;
  path: string;
  inMemory: boolean;
  /** What it was given, or "On disk". */
  size: string;
  /** "Auto", or the fitted-down size when it was given less than it asked for. */
  tags: string[];
  /** Why it is on disk, in a sentence; empty for a folder in memory. */
  note: string;
}

export interface MemoryBadge {
  key: BadgeKey;
  tone: BadgeTone;
  /** The glyphs on the pill. The folders badge draws one per folder in memory. */
  icons: BadgeIcon[];
  /** The few words on the pill. */
  label: string;
  /** A badge for something that has not happened -- what an observing valve would do. */
  dashed: boolean;
  /** The runner wants more memory and cannot have it: a warning dot on the pill. */
  wanting: boolean;
  eyebrow: string;
  title: string;
  detail: string;
  figures: BadgeFigure[];
  bar?: BadgeBar;
  notes: string[];
  folders: FolderRow[];
  /** The whole card as one sentence: what assistive technology gets. */
  text: string;
}

type Source = {
  memory_resource?: MemoryResourceState;
  scratch?: RunnerScratch;
};

const mb = formatMegabytes;

/** "once", "twice", "5 times": a count in the words a sentence wants. */
export function times(n: number): string {
  if (n === 1) return 'once';
  if (n === 2) return 'twice';
  return `${n} times`;
}

/** The valve's own line above a card's title. */
function eyebrowOf(mode: MemoryResourceState['mode']): string {
  switch (mode) {
    case 'automatic':
      return 'Memory valve · Automatic';
    case 'observe':
      return 'Memory valve · Observing';
    default:
      // A loan stands after the pool switches the valve off, and is still the
      // runner's; saying the valve is "off" without that would read as no loan.
      return 'Memory valve · now off for this pool';
  }
}

/** A fragment from the agent, as a sentence: "raised the limit from 1 to 2 MB: ..." */
export function sentenceCase(text: string): string {
  const t = text.trim();
  if (t === '') return '';
  const s = t.charAt(0).toUpperCase() + t.slice(1);
  return /[.!?]$/.test(s) ? s : `${s}.`;
}

/** Why a runner that wanted more memory did not get it, as a sentence about this runner. */
export function blockedWords(v: MemoryResourceState): string {
  switch (v.blocked) {
    case 'at_ceiling':
      return `It holds ${mb(v.current_mb)}, the most this runner may hold.`;
    case 'pool_empty':
      return 'Its host had no spare memory left to lend.';
    case 'host_floor':
      return 'Lending more would have left its host less free memory than it keeps back for itself.';
    case 'unmeasured':
      return "Its host's free memory could not be read, so none is lent: a loan that cannot be checked is not made.";
    case 'unsupported':
      return 'The container runtime on its host cannot change a limit while a job runs, so the valve cannot help here.';
    case 'failed':
      return 'The container runtime refused to raise the limit; the valve will try again.';
    default:
      return '';
  }
}

/** The pill's own words for a blocked runner, which has to fit in a table cell. */
function blockedLabel(blocked: NonNullable<MemoryResourceState['blocked']>): string {
  switch (blocked) {
    case 'at_ceiling':
      return 'At ceiling';
    case 'pool_empty':
    case 'host_floor':
      return 'Host full';
    case 'unmeasured':
      return 'Unmeasured';
    case 'unsupported':
      return 'Unsupported';
    default:
      return 'Raise failed';
  }
}

function blockedTone(blocked: NonNullable<MemoryResourceState['blocked']>): BadgeTone {
  switch (blocked) {
    case 'failed':
      return 'danger';
    case 'unmeasured':
    case 'unsupported':
      return 'neutral';
    default:
      return 'pending';
  }
}

function barOf(v: MemoryResourceState): BadgeBar {
  return {
    guaranteed: v.guaranteed_mb,
    lent: Math.max(v.lent_mb, 0),
    swap: Math.max(v.spill_mb ?? 0, 0),
    ceiling: Math.max(v.ceiling_mb, v.current_mb),
  };
}

function figuresOf(v: MemoryResourceState): BadgeFigure[] {
  const out: BadgeFigure[] = [
    { label: 'Guaranteed', value: mb(v.guaranteed_mb) },
    { label: 'Current', value: mb(v.current_mb) },
    { label: 'Ceiling', value: mb(v.ceiling_mb) },
  ];
  if ((v.spill_mb ?? 0) > 0) out.push({ label: 'Swap', value: mb(v.spill_mb) });
  return out;
}

/** The sentence a card is, for assistive technology. */
function sentence(b: Omit<MemoryBadge, 'text'>): string {
  const parts = [`${b.eyebrow}.`, `${b.title}.`, b.detail, ...b.notes];
  if (b.figures.length > 0)
    parts.push(b.figures.map((f) => `${f.label} ${f.value}`).join(', ') + '.');
  for (const f of b.folders) {
    parts.push(
      f.inMemory
        ? `${f.label} at ${f.path}: ${f.size} in memory${f.tags.length > 0 ? ` (${f.tags.join(', ')})` : ''}.`
        : `${f.label} at ${f.path}: on disk. ${f.note}`,
    );
  }
  return parts
    .filter((p) => p.trim() !== '')
    .join(' ')
    .replace(/\s+/g, ' ');
}

function finish(b: Omit<MemoryBadge, 'text'>): MemoryBadge {
  return { ...b, text: sentence(b) };
}

const empty = {
  dashed: false,
  wanting: false,
  figures: [] as BadgeFigure[],
  notes: [] as string[],
  folders: [] as FolderRow[],
};

/**
 * The badges for what the valve has done for a runner, most important first.
 *
 * `quiet` is whether to say so when it has done nothing: the runner's page
 * wants to say the valve is watching, and the grid would be a column of the same
 * pill down every row of a pool that uses it.
 */
function valveBadges(v: MemoryResourceState, quiet: boolean): MemoryBadge[] {
  const out: MemoryBadge[] = [];
  const bar = barOf(v);
  const wanting = v.blocked !== undefined && v.blocked !== 'unsupported';
  // The agent's own sentence says why with the numbers it had; the general words
  // stand in for it where it said none, so a card never has a blocked runner with
  // no reason.
  const reason = sentenceCase(v.reason ?? '');
  const notes: string[] = [];
  if (reason !== '') notes.push(reason);
  else if (v.blocked) notes.push(blockedWords(v));
  if (v.near_limit) notes.push('It has come within a tenth of a memory limit at some point.');
  const eyebrow = eyebrowOf(v.mode);
  const lent = v.lent_mb;
  const spill = v.spill_mb ?? 0;
  const raised =
    (v.raises ?? 0) > 0
      ? `its limit was raised ${times(v.raises ?? 0)}`
      : 'its limit was raised while the job ran';

  if (lent > 0) {
    out.push(
      finish({
        ...empty,
        key: 'memory',
        tone: 'accent',
        icons: ['memory'],
        label: `+${mb(lent)}`,
        eyebrow,
        title: `Lent ${mb(lent)} of memory`,
        detail:
          `It was created with ${mb(v.guaranteed_mb)} and now holds ${mb(v.current_mb)}: ${raised}, out of memory its host had not promised to any runner. ` +
          'A loan is never taken back while the runner lives, because lowering a limit is what kills the job it was meant to save.',
        figures: figuresOf(v),
        bar,
        notes,
        wanting,
      }),
    );
  }

  if (spill > 0) {
    out.push(
      finish({
        ...empty,
        key: 'swap',
        tone: 'pending',
        icons: ['swap'],
        label: 'Swap',
        eyebrow,
        title: `May use ${mb(spill)} of swap`,
        detail:
          'Its limit could not be raised any further and it was pressing against it, so the kernel may now put what does not fit in swap on its host. ' +
          'The job slows down instead of being killed.',
        figures: lent > 0 ? [] : figuresOf(v),
        bar: lent > 0 ? undefined : bar,
        notes: [
          ...((v.spill_allowed_mb ?? 0) > 0
            ? [`This pool allows each container up to ${mb(v.spill_allowed_mb)} of swap.`]
            : []),
          ...(lent > 0 ? [] : notes),
        ],
        wanting: lent === 0 && wanting,
      }),
    );
  }

  if (lent === 0 && spill === 0) {
    if (v.state === 'observing' || v.mode === 'observe') {
      const would = v.would_lend_mb ?? 0;
      if (would > 0 || v.near_limit || (v.would_spill_mb ?? 0) > 0) {
        const wouldSpill = v.would_spill_mb ?? 0;
        out.push(
          finish({
            ...empty,
            key: 'observing',
            tone: 'neutral',
            icons: ['eye'],
            label: would > 0 ? `~${mb(would)}` : 'Near limit',
            dashed: true,
            eyebrow,
            title: would > 0 ? `Would have lent ${mb(would)}` : 'Came near its memory limit',
            detail:
              "This pool's valve is only observing: it works out what it would do and changes nothing, so the runner still has exactly the memory it was created with. " +
              'Set the pool to automatic and it will act.',
            figures: [
              { label: 'Guaranteed', value: mb(v.guaranteed_mb) },
              ...(would > 0 ? [{ label: 'Would hold', value: mb(v.guaranteed_mb + would) }] : []),
              { label: 'Ceiling', value: mb(v.ceiling_mb) },
              ...(wouldSpill > 0 ? [{ label: 'Would allow swap', value: mb(wouldSpill) }] : []),
            ],
            notes: reason !== '' ? [reason] : [],
          }),
        );
      } else if (quiet) {
        out.push(watching(v, eyebrow));
      }
    } else if (v.blocked) {
      out.push(
        finish({
          ...empty,
          key: 'capped',
          tone: blockedTone(v.blocked),
          icons: [v.blocked === 'at_ceiling' ? 'ceiling' : 'alert'],
          label: blockedLabel(v.blocked),
          eyebrow,
          title: capTitle(v),
          detail: capDetail(v),
          figures: figuresOf(v),
          bar,
          notes,
          wanting,
        }),
      );
    } else if (quiet) {
      out.push(watching(v, eyebrow));
    }
  }
  return out;
}

function capTitle(v: MemoryResourceState): string {
  switch (v.blocked) {
    case 'at_ceiling':
      return 'At the most it may hold';
    case 'pool_empty':
    case 'host_floor':
      return 'Its host had no memory to lend';
    case 'unmeasured':
      return "Its host's memory could not be measured";
    case 'unsupported':
      return 'This runtime cannot raise a live limit';
    default:
      return 'The limit could not be raised';
  }
}

function capDetail(v: MemoryResourceState): string {
  switch (v.blocked) {
    case 'at_ceiling':
      return `It wanted more memory and holds ${mb(v.current_mb)}, the most this runner may. Raise the pool's memory ceiling to let a job like it use more.`;
    case 'pool_empty':
    case 'host_floor':
      return 'It wanted more memory than it was created with, and its host had none to spare: every runner there is promised its own share, and the host keeps some back for itself. A smaller share per runner, or fewer slots, leaves more to lend.';
    case 'unmeasured':
      return "The valve lends only memory it can check is free, and this host's could not be read. Nothing was lent.";
    case 'unsupported':
      return 'The container runtime on its host refused to change a limit that is already set, so this runner keeps the memory it was created with. It is not asked again.';
    default:
      return 'The runtime refused to raise the limit. The valve asks again while the job runs.';
  }
}

function watching(v: MemoryResourceState, eyebrow: string): MemoryBadge {
  return finish({
    ...empty,
    key: 'watching',
    tone: 'neutral',
    icons: ['memory'],
    label: 'Watching',
    eyebrow,
    title: 'Watching its memory',
    detail:
      'The valve is on for this pool and has had nothing to do: the runner has stayed well inside its limit, so it holds only what it was created with.',
    figures: figuresOf(v),
    bar: barOf(v),
  });
}

/* -- folders ---------------------------------------------------------------- */

const folderIcon: Record<ScratchFolder['kind'], BadgeIcon> = {
  work: 'folder',
  tmp: 'tmp',
  daemon: 'image',
};

function folderRow(f: ScratchFolder): FolderRow {
  const tags: string[] = [];
  if (f.in_memory) {
    if (f.auto) tags.push('Auto');
    if ((f.asked_mb ?? 0) > (f.size_mb ?? 0) && (f.size_mb ?? 0) > 0) {
      tags.push(`fitted to its limit from ${mb(f.asked_mb)}`);
    }
  }
  return {
    kind: f.kind,
    icon: folderIcon[f.kind] ?? 'folder',
    label: f.label,
    path: f.path,
    inMemory: f.in_memory,
    size: f.in_memory ? mb(f.size_mb) : 'On disk',
    tags,
    note: f.in_memory ? '' : (f.note ?? ''),
  };
}

function folderBadge(scratch: RunnerScratch): MemoryBadge | null {
  const folders = scratch.folders ?? [];
  if (folders.length === 0) return null;
  const rows = folders.map(folderRow);
  const inMemory = rows.filter((r) => r.inMemory);
  const total = folders.reduce((sum, f) => sum + (f.in_memory ? (f.size_mb ?? 0) : 0), 0);
  const some = inMemory.length > 0;
  const all = inMemory.length === rows.length;
  const count = `${inMemory.length} of ${rows.length} ${rows.length === 1 ? 'folder' : 'folders'}`;
  return finish({
    ...empty,
    key: 'folders',
    tone: 'neutral',
    icons: some ? inMemory.map((r) => r.icon) : ['disk'],
    label: some ? mb(total) : 'On disk',
    dashed: !some,
    eyebrow: 'In-memory folders',
    title: all
      ? rows.length === 1
        ? `${rows[0]?.label} is in memory`
        : `All ${rows.length} folders are in memory`
      : some
        ? `${count} in memory`
        : 'Kept on disk, though its pool asked for memory',
    detail: some
      ? "These live in the runner's memory instead of on its host's disk, which is faster and is charged to the runner's memory limit."
      : "The pool keeps these folders in memory where it can; for this runner it could not, so they are on its host's disk, as they would be with the setting off.",
    folders: rows,
  });
}

/**
 * The badges for one runner: what the valve has done, then its folders.
 *
 * The valve's come first because they change and the folders do not, and a pill
 * that moves belongs at the edge a reader's eye is already on.
 */
export function memoryBadges(runner: Source, options: { quiet?: boolean } = {}): MemoryBadge[] {
  const out: MemoryBadge[] = [];
  if (runner.memory_resource)
    out.push(...valveBadges(runner.memory_resource, options.quiet ?? false));
  const folders = runner.scratch ? folderBadge(runner.scratch) : null;
  if (folders) out.push(folders);
  return out;
}

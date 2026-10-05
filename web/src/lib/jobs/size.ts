/**
 * What the controller recorded about a job's size, as the drawer and the grid say
 * it.
 *
 * Three things are kept apart because they are three different facts and an
 * operator asks them in order: which class the job was put in and why, which
 * pool it was sent to while it waited (not always the same, when its own class
 * had nowhere to run it), and which class of host actually took it (not always
 * either, because for a job that asked only for the base label GitHub, not the
 * controller, chooses the runner). The sentences that explain a decision are the
 * controller's; this is the arrangement and the one sentence that is the UI's,
 * the difference between a deliberate fallback and a job that landed elsewhere.
 */
import type { Job, LabelAdvice, SizeClass } from '../api/types';
import { classWord } from '../hosts/tags';

export type SizeBasis = NonNullable<Job['size_basis']>;

const BASIS: Record<SizeBasis, string> = {
  explicit: 'Asked for by name',
  pin: 'Pinned by an operator',
  history: 'From its earlier runs',
  default: 'The default',
};

export interface JobSizeView {
  class: SizeClass;
  /** `Medium`, for a badge. */
  word: string;
  basis: SizeBasis | undefined;
  basisWords: string;
  /** "Classed medium because ...", whole. Empty where the controller gave no reason. */
  because: string;
  /** Present only when the job was sent to a class other than its own. */
  fallback: { to: SizeClass; note: string } | null;
  /** Present only once a runner has taken the job. */
  ran: { class: SizeClass; asExpected: boolean; text: string } | null;
  /**
   * The share of its CPU periods the runner was held back in, as a whole
   * percentage, or null where the agent never sampled it.
   */
  throttledPercent: number | null;
}

/**
 * The size facts of one job, or null for a job nobody classed -- which is every
 * job while `scheduler.size_routing` is `off`, so a drawer on a fleet that has
 * not turned this on is the drawer it always was.
 */
export function jobSize(job: Job): JobSizeView | null {
  const size = job.size_class;
  if (!size) return null;
  const basis = job.size_basis;
  const routed = job.routed_class;
  const fallback = routed && routed !== size ? { to: routed, note: job.routed_note ?? '' } : null;
  return {
    class: size,
    word: classWord(size),
    basis,
    basisWords: basis ? BASIS[basis] : '',
    because: job.size_reason ? `Classed ${size} because ${job.size_reason}.` : '',
    fallback,
    ran: job.ran_class ? ranOn(job.ran_class, routed ?? size, size, fallback !== null) : null,
    throttledPercent:
      job.throttled_share === null || job.throttled_share === undefined
        ? null
        : Math.round(job.throttled_share * 100),
  };
}

function ranOn(ran: SizeClass, expected: SizeClass, own: SizeClass, fellBack: boolean) {
  if (ran === expected) {
    return {
      class: ran,
      asExpected: true,
      text: fellBack
        ? `Ran on a ${ran} host, the class it was sent to instead of ${own}.`
        : `Ran on a ${ran} host, the class it was put in.`,
    };
  }
  // Not a fault and not something to retry: the controller makes the runner for a
  // class, and GitHub decides which waiting job an idle runner takes. The
  // sentence is the one the documentation gives, because it is the question an
  // operator arrives with.
  return {
    class: ran,
    asExpected: false,
    text: `Ran on a ${ran} host, not the ${expected} one it was ${fellBack ? 'sent to' : 'put in'}. A job that names only the base label is matched to a runner by GitHub, so it can land on one made for another class. Write the class label in its runs-on to make it a guarantee.`,
  };
}

/** `Medium`, or `Medium → Large` where the job ran on a class other than its own. */
export function classCell(job: Job): string {
  const size = job.size_class;
  if (!size) return '';
  const ran = job.ran_class;
  return ran && ran !== size ? `${classWord(size)} → ${classWord(ran)}` : classWord(size);
}

/* -- label advice ----------------------------------------------------------- */

const ADVICE: Record<LabelAdvice['kind'], { label: string; hint: string }> = {
  too_small: {
    label: 'Names a class that is too small',
    hint: 'It can only run on hosts smaller than its runs need, and is killed when it needs more than they have.',
  },
  unguaranteed: {
    label: 'Names no class',
    hint: 'It needs more than the default class, so it is routed there on a best-effort basis. A class label makes that a guarantee.',
  },
  too_large: {
    label: 'Names a class that is larger than it uses',
    hint: 'It occupies a host another job needs. Keep it if the job needs the host for something that is not measured, such as its disk or network.',
  },
};

/** What a kind of advice means. A kind this build has not heard of is shown as it came. */
export function adviceWords(kind: string): { label: string; hint: string } {
  return ADVICE[kind as LabelAdvice['kind']] ?? { label: kind, hint: '' };
}

/** The key a pin and an advice row share, so a row knows whether it is already pinned. */
export function pinKey(row: { repo: string; workflow?: string; job_name?: string }): string {
  return [row.repo.toLowerCase(), row.workflow ?? '', row.job_name ?? ''].join('\u0000');
}

/** What a pin covers, in words: the whole repository, or one job of one workflow. */
export function pinScope(row: { repo: string; workflow?: string; job_name?: string }): string {
  if (row.workflow || row.job_name) {
    return `${row.repo} · ${row.workflow ?? 'any workflow'} · ${row.job_name ?? 'any job'}`;
  }
  return `${row.repo} · every job`;
}

/**
 * A sentence the controller wrote to continue a line, begun with a capital.
 * Its advice is written to follow the job's name -- "its runs-on asks for ..." --
 * and reads as a fragment when it stands alone under it.
 */
export function sentence(text: string): string {
  return text === '' ? text : text.charAt(0).toUpperCase() + text.slice(1);
}

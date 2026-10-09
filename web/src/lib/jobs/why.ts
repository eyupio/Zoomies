// The pure half of the drawer's Why section: how a next step becomes a
// button, a link or a row, and how a piece of evidence reads. Kept out of the
// component so a unit test can hold it without rendering anything.

export interface WhyEvidence {
  kind: string;
  label: string;
  value: string;
  unit?: string;
  ref?: string;
}

export interface WhyStep {
  text: string;
  kind: string;
  link?: string;
}

export interface StepAction {
  label: string;
  /** The drawer's own re-run button. */
  rerun?: boolean;
  /** Somewhere to go: a path in this UI, or a URL. */
  href?: string;
  /** True for a URL, which opens in a new tab; false for a path here. */
  external?: boolean;
}

/** The evidence kinds whose value is a figure, read in a monospace column. */
const FIGURES = new Set(['exit_code', 'signal', 'memory_limit', 'memory_peak', 'queue_wait']);

export function stepAction(step: WhyStep): StepAction {
  if (step.kind === 'rerun') return { label: step.text, rerun: true };
  if (step.link) {
    return { label: step.text, href: step.link, external: /^https?:\/\//.test(step.link) };
  }
  return { label: step.text };
}

export interface EvidenceRow {
  label: string;
  value: string;
  mono: boolean;
  href?: string;
}

export function evidenceRows(evidence: readonly WhyEvidence[]): EvidenceRow[] {
  return evidence.map((e) => {
    const row: EvidenceRow = {
      label: e.label,
      value: e.unit ? `${e.value} ${e.unit}` : e.value,
      mono: FIGURES.has(e.kind),
    };
    if (e.ref) row.href = e.ref;
    return row;
  });
}

/** The catalog page a problem code reads on at, from the controller's own "read" step. */
export function catalogLink(steps: readonly WhyStep[]): string | null {
  for (const s of steps) {
    if (s.kind === 'read' && s.link && /^https?:\/\//.test(s.link) && s.link.includes('#')) {
      return s.link;
    }
  }
  return null;
}

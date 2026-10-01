/**
 * What the first job to finish on a runner of the operator's own deserves said
 * about it.
 *
 * The setup checklist used to mark the best moment in the product by
 * disappearing. This is the sentence that replaces the silence, kept apart from
 * the component so the part that can be wrong -- which jobs count, and which
 * do not get cheered -- is a function a test can read.
 *
 * Three answers, because there are three things an event can mean. It is not
 * the moment yet (a job still queued or running, or one no runner of ours
 * took), so keep watching. It is the moment and it went well, so say so. Or it
 * is the moment and it did not, in which case the watch is over and nothing is
 * said: a first job that failed is the Jobs page's to explain, and a toast that
 * cheered it would be the wrong note.
 */
import type { Job } from '$lib/api/types';
import { formatDuration } from '$lib/format';

export type FirstJob =
  { kind: 'waiting' } | { kind: 'finished'; toast: { title: string; message: string } | null };

export function judgeFirstJob(
  job: Pick<Job, 'state' | 'runner_id' | 'runner_name' | 'conclusion' | 'duration_ms'>,
): FirstJob {
  if (job.state !== 'completed' || !job.runner_id) return { kind: 'waiting' };
  if (job.conclusion !== 'success') return { kind: 'finished', toast: null };
  const took = job.duration_ms ? ` in ${formatDuration(job.duration_ms)}` : '';
  return {
    kind: 'finished',
    toast: {
      title: 'Your first job ran on your own runner',
      message: `${job.runner_name || 'A runner'} finished it${took}. The setup checklist has done its job; the fleet's numbers fill in from here.`,
    },
  };
}

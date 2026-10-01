/**
 * The smallest workflow that proves a fleet works.
 *
 * A new operator's first job used to need a real repository's workflow edited
 * and pushed, or the migration wizard, which opens pull requests on real
 * repositories. Neither is a thing to do to find out whether the thing you have
 * just installed runs a job at all. This is the alternative: a file that
 * touches nothing of theirs, starts by hand, and prints the one fact that says
 * where it ran.
 *
 * `workflow_dispatch` and not a push trigger, so the file does not fire on every
 * commit afterwards -- it is a probe, and it should sit there until somebody
 * presses the button.
 */
import { runsOn } from '../brand';

/** What the Actions tab lists it as. */
export const TEST_JOB_NAME = 'Zoomies test job';

/** Where it goes in a repository. */
export const TEST_JOB_FILE = '.github/workflows/zoomies-test.yml';

/**
 * The complete file for a pool with these labels.
 *
 * It asks for the pool by the same `runs-on` value everything else in the
 * product prints, so a pool labelled with several labels gets the list form and
 * the job reaches that pool and no other.
 */
export function testJobWorkflow(labels: readonly string[]): string {
  return [
    `name: ${TEST_JOB_NAME}`,
    'on: workflow_dispatch',
    'jobs:',
    '  hello:',
    `    runs-on: ${runsOn(labels)}`,
    '    steps:',
    // The sleep is the point of the third line: a job that finishes in
    // milliseconds is over before an operator has looked from GitHub to this
    // page, and they would see a runner appear and vanish without ever having
    // seen it busy.
    '      - run: |',
    '          echo "Hello from $(hostname)"',
    '          uname -a',
    '          sleep 5',
    '',
  ].join('\n');
}

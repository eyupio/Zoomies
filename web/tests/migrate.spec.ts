/**
 * Moving a repository's CI onto this fleet.
 *
 * The demo installation has no GitHub App behind it, which makes it exactly the
 * right fixture for this: the reads answer, so the wizard can be walked to the
 * screen that matters, and the App has none of the three permissions a pull
 * request needs -- so the last step must refuse rather than get halfway.
 */
import { expect, test } from '@playwright/test';
import { FIXTURE, browserOverride, goto, navEntry, pageHeading } from './support/fixtures';

test.use(browserOverride);

test.skip(({ isMobile }) => isMobile, 'the wizard interactions are covered by the mobile suite');

test('an empty first batch continues scanning and finds repositories on later pages', async ({
  page,
}) => {
  let releasePage!: () => void;
  const gate = new Promise<void>((resolve) => (releasePage = resolve));
  await page.route('**/api/v1/migrations/plan', async (route) => {
    const response = await route.fetch({
      postData: { installation_id: route.request().postDataJSON().installation_id },
    });
    const plan = await response.json();
    const cursor = route.request().postDataJSON().cursor;
    if (!cursor) {
      await route.fulfill({
        json: {
          ...plan,
          repositories: [{ repo: 'acme/docs', workflows: [] }],
          total_repos: 2,
          next_cursor: 'second',
        },
      });
    } else {
      await gate;
      await route.fulfill({
        json: {
          ...plan,
          repositories: plan.repositories.filter(
            (r: { repo: string }) => r.repo === FIXTURE.repos[0],
          ),
          total_repos: 2,
          next_cursor: '',
        },
      });
    }
  });
  await walkTo(page, 1);
  await expect(page.getByText('No matches in the repositories checked so far')).toBeVisible();
  await expect(page.getByText('1 of 2 repositories checked so far')).toBeVisible();
  await expect(page.getByText('No repositories to migrate', { exact: true })).toBeHidden();
  releasePage();
  await expect(page.getByText('Repository scan complete')).toBeVisible();
  await expect(page.getByRole('checkbox', { name: FIXTURE.repos[0], exact: false })).toBeChecked();
  await expect(page.getByRole('button', { name: 'Next', exact: true })).toBeEnabled();
});

test('a failed later batch keeps choices and can resume without selecting a cleared repository again', async ({
  page,
}) => {
  let calls = 0;
  await page.route('**/api/v1/migrations/plan', async (route) => {
    calls += 1;
    if (calls === 2) {
      await route.fulfill({
        status: 503,
        json: { error: { code: 'unavailable', message: 'Try the remaining repositories again.' } },
      });
      return;
    }
    const response = await route.fetch({
      postData: { installation_id: route.request().postDataJSON().installation_id },
    });
    const plan = await response.json();
    const first = plan.repositories.find((r: { repo: string }) => r.repo === FIXTURE.repos[0]);
    const second = { ...first, repo: 'acme/later' };
    await route.fulfill({
      json: {
        ...plan,
        repositories: calls === 1 ? [first] : [first, second],
        total_repos: 2,
        next_cursor: calls === 1 ? 'second' : '',
      },
    });
  });
  await walkTo(page, 1);
  await expect(page.getByRole('button', { name: 'Scan remaining repositories' })).toBeVisible();
  const first = page.getByRole('checkbox', { name: FIXTURE.repos[0], exact: false });
  await first.uncheck();
  await page.getByRole('button', { name: 'Scan remaining repositories' }).click();
  await expect(page.getByText('Repository scan complete')).toBeVisible();
  await expect(first).not.toBeChecked();
  await expect(page.getByRole('checkbox', { name: 'acme/later', exact: false })).toBeChecked();
});

test('large scans keep the migration within the API limit and search preserves the selection', async ({
  page,
}) => {
  await page.route('**/api/v1/migrations/plan', async (route) => {
    const response = await route.fetch({
      postData: { installation_id: route.request().postDataJSON().installation_id },
    });
    const plan = await response.json();
    const example = plan.repositories.find((r: { repo: string }) => r.repo === FIXTURE.repos[0]);
    await route.fulfill({
      json: {
        ...plan,
        repositories: Array.from({ length: 30 }, (_, n) => ({
          ...example,
          repo: `acme/project-${String(n).padStart(2, '0')}`,
        })),
        total_repos: 30,
        next_cursor: '',
      },
    });
  });
  await walkTo(page, 1);
  const list = page.getByRole('list', { name: 'Checked repositories' });
  await expect(list.getByRole('checkbox', { checked: true })).toHaveCount(25);
  await expect(page.getByText(/This batch is full/)).toBeVisible();
  await page.getByRole('searchbox', { name: 'Search checked repositories' }).fill('project-29');
  const last = list.getByRole('checkbox', { name: 'acme/project-29', exact: false });
  await expect(last).toBeDisabled();
  await page.getByRole('searchbox', { name: 'Search checked repositories' }).clear();
  await list.getByRole('checkbox', { name: 'acme/project-00', exact: false }).uncheck();
  await last.check();
  await expect(list.getByRole('checkbox', { checked: true })).toHaveCount(25);
});

/**
 * The `build` job of one repository on the exceptions step. Every demo
 * repository has the same workflow, so the repository has to be named.
 */
function buildJob(page: import('@playwright/test').Page) {
  return page.getByLabel('Where build in .github/workflows/ci.yml runs, in acme/widgets');
}

/** Walk to a step, leaving the wizard on it. */
async function walkTo(page: import('@playwright/test').Page, step: number): Promise<void> {
  await goto(page, '/migrate', 'Migrate repositories');
  // One installation, so it is already chosen.
  await expect(page.getByRole('radio', { name: 'acme', exact: false })).toBeChecked();
  for (let i = 0; i < step; i++) {
    await page.getByRole('button', { name: 'Next' }).click();
  }
}

test('the navigation reaches the wizard', async ({ page }) => {
  await goto(page, '/', 'Overview');
  await navEntry(page, '/migrate').click();
  await expect(pageHeading(page, 'Migrate repositories')).toBeVisible();
});

test('the scan lists every repository and says what each one would get', async ({ page }) => {
  await walkTo(page, 1);

  await expect(page.getByRole('heading', { level: 2, name: 'Repositories' })).toBeVisible();
  for (const repo of FIXTURE.repos) {
    // Each is ticked, because each has a job that would move.
    await expect(page.getByRole('checkbox', { name: repo, exact: false })).toBeChecked();
  }
  // The count is the scan's own, not a guess.
  await expect(page.getByText('1 job in 1 file').first()).toBeVisible();
});

test('a repository with nothing to move is hidden, and can be shown', async ({ page }) => {
  await walkTo(page, 1);

  // The default: the repositories that would change, and a count of what that
  // hides -- not a list of an organisation's documentation repositories.
  const quiet = page.getByRole('checkbox', { name: FIXTURE.quietRepo, exact: false });
  await expect(quiet).toBeHidden();
  await expect(page.getByText('that cannot move')).toBeVisible();

  await page.getByRole('switch', { name: 'Only repositories with something to move' }).click();
  await expect(quiet).toBeVisible();
  // It is listed so the operator can see it was looked at, and it cannot be
  // chosen, because there is nothing in it to choose.
  await expect(quiet).toBeDisabled();
  await expect(page.getByText('No workflows', { exact: true })).toBeVisible();
});

test('a repository nothing could be opened against cannot be chosen', async ({ page }) => {
  await walkTo(page, 1);

  // Both are hidden with everything else that cannot move.
  const archived = page.getByRole('checkbox', { name: FIXTURE.archivedRepo, exact: false });
  const migrated = page.getByRole('checkbox', { name: FIXTURE.migratedRepo, exact: false });
  await expect(archived).toBeHidden();
  await expect(migrated).toBeHidden();

  await page.getByRole('switch', { name: 'Only repositories with something to move' }).click();

  // Archived is the one that used to be ticked and walked all the way to the
  // results before saying no: GitHub refuses every write to it.
  await expect(archived).toBeDisabled();
  await expect(archived).not.toBeChecked();
  await expect(page.getByText('Archived — accepts no pull requests')).toBeVisible();

  // Already migrated is a different answer from "nothing to move", and the
  // list says which it is rather than leaving an operator to guess. Anchored,
  // because the row above the list says the same words.
  await expect(migrated).toBeDisabled();
  await expect(migrated).not.toBeChecked();
  await expect(page.getByText(/^Already on Zoomies/)).toBeVisible();

  // Ticking everything still leaves them alone. The wizard builds the
  // pull-request call from the same rule the list disables rows with, so
  // neither of them can reach it.
  const all = page.getByRole('checkbox', { name: 'Select every file that could move' });
  await all.click(); // clears the default selection
  await all.click(); // and selects everything on offer
  await expect(page.getByRole('checkbox', { name: FIXTURE.repos[0], exact: false })).toBeChecked();
  await expect(archived).not.toBeChecked();
  await expect(migrated).not.toBeChecked();
});

test('a repository with several workflows is chosen file by file', async ({ page }) => {
  await walkTo(page, 1);

  const repo = page.getByRole('checkbox', { name: FIXTURE.multiWorkflowRepo, exact: false });
  await expect(repo).toBeChecked();

  await page.getByRole('button', { name: `2 files in ${FIXTURE.multiWorkflowRepo}` }).click();
  const release = page.getByRole('checkbox', { name: '.github/workflows/release.yml' });
  await expect(release).toBeChecked();
  await release.click();

  // The repository is now partly chosen, and says so.
  await expect(repo).not.toBeChecked();
  await expect(page.getByText('1 of 2 files chosen')).toBeVisible();

  // And the review only offers to change the file that is still ticked.
  // Three steps on: labels, exceptions, review.
  for (let i = 0; i < 3; i++) await page.getByRole('button', { name: 'Next' }).click();
  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();
  await expect(page.getByText('.github/workflows/release.yml')).toBeHidden();
  await expect(page.getByText('.github/workflows/ci.yml').first()).toBeVisible();
});

test('the mapping step proposes the pool that matches the hosted label', async ({ page }) => {
  await walkTo(page, 2);

  await expect(page.getByRole('heading', { level: 2, name: 'Labels' })).toBeVisible();
  await expect(page.getByText('ubuntu-latest', { exact: true })).toBeVisible();

  // ubuntu-latest is x64 Linux, and so is exactly one of the two demo pools.
  const select = page.getByLabel('What replaces ubuntu-latest');
  await expect(select).toHaveValue(FIXTURE.linuxPool);
  // Leaving it alone is always an option, and it is a real one.
  await expect(select.getByRole('option', { name: /Leave it alone/ })).toHaveCount(1);
});

test('the exceptions step offers a pool for one job without touching the rest', async ({
  page,
}) => {
  await walkTo(page, 3);

  await expect(page.getByRole('heading', { level: 2, name: 'Exceptions' })).toBeVisible();

  // The default is the answer the label mapping already gave, so an operator
  // who wants one answer everywhere reads past this step and changes nothing.
  const build = buildJob(page);
  await expect(build).toHaveValue('');
  await expect(
    build.getByRole('option', { name: `Use the label mapping — ${FIXTURE.linuxPool}` }),
  ).toHaveCount(1);
  // Every pool is a choice for this one job, and so is staying on GitHub.
  await expect(build.getByRole('option', { name: `${FIXTURE.armPool} — the` })).toHaveCount(1);
  await expect(build.getByRole('option', { name: 'Leave this job where it is' })).toHaveCount(1);

  // The matrix job cannot be pointed anywhere from here: what it resolves to is
  // decided elsewhere in the file, so it is listed with the reason rather than
  // as a select that would quietly do nothing.
  await expect(page.getByText('${{ }} expression').first()).toBeVisible();
  await expect(page.getByLabel(/Where matrix in/)).toHaveCount(0);
});

test('an exception reaches the diff, and reaches only that job', async ({ page }) => {
  await walkTo(page, 3);

  await buildJob(page).selectOption(FIXTURE.armPool);
  await page.getByRole('button', { name: 'Next' }).click();

  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();
  // One file got the exception. Every other file has a job on the same label
  // and still gets the label mapping's answer, which is the whole point of the
  // step being per job rather than a second mapping.
  //
  // Counted against the total rather than a number, so that a repository or a
  // workflow added to the fixture later does not silently make this weaker.
  // Each file in the fixture carries one job that would move.
  const diffs = page.getByRole('group', { name: 'The change to this file' });
  const total = await diffs.count();
  expect(total).toBeGreaterThan(1);
  await expect(diffs.filter({ hasText: `+    runs-on: ${FIXTURE.armPool}` })).toHaveCount(1);
  await expect(diffs.filter({ hasText: `+    runs-on: ${FIXTURE.linuxPool}` })).toHaveCount(
    total - 1,
  );

  // And the review says which line was decided by hand, so a diff that does not
  // match the label mapping is never a surprise.
  await expect(page.getByText(`build → ${FIXTURE.armPool}`)).toBeVisible();
});

test('the review step shows the diff and the jobs it will not touch', async ({ page }) => {
  await walkTo(page, 4);

  await expect(page.getByRole('heading', { level: 2, name: 'Review' })).toBeVisible();

  // The exact change, as a diff -- not a sentence describing one.
  const diff = page.getByRole('group', { name: 'The change to this file' }).first();
  await expect(diff).toContainText('-    runs-on: ubuntu-latest');
  await expect(diff).toContainText(`+    runs-on: ${FIXTURE.linuxPool}`);

  // And the job it deliberately leaves behind, with the reason.
  await page
    .getByRole('group', { name: 'The change to this file' })
    .first()
    .scrollIntoViewIfNeeded();
  const skips = page.getByText('left alone in this repository').first();
  await expect(skips).toBeVisible();
  await skips.click();
  await expect(page.getByText('${{ }} expression').first()).toBeVisible();
});

test('an App without the permissions is stopped here, not halfway through', async ({ page }) => {
  await walkTo(page, 4);

  const blocker = page.getByRole('alert');
  await expect(blocker).toContainText('cannot open a pull request yet');
  // Each missing permission is named the way GitHub's own settings page names
  // it, so the fix is a search on that page rather than a guess.
  for (const name of ['Contents', 'Pull requests', 'Workflows']) {
    await expect(blocker).toContainText(name);
  }
  await expect(blocker.getByRole('link', { name: /Open the App's permissions/ })).toHaveAttribute(
    'href',
    /settings\/apps\//,
  );

  // Nothing can be opened while that is true.
  await expect(page.getByRole('button', { name: 'Open the pull requests' })).toBeDisabled();
});

test('pausing a multi-page scan finishes the current batch and keeps unchecked repositories explicit', async ({
  page,
}) => {
  let releaseBatch!: () => void;
  const gate = new Promise<void>((resolve) => (releaseBatch = resolve));
  const cursors: string[] = [];
  await page.route('**/api/v1/migrations/plan', async (route) => {
    const request = route.request().postDataJSON();
    const cursor = request.cursor ?? '';
    cursors.push(cursor);
    const response = await route.fetch({ postData: { installation_id: request.installation_id } });
    const plan = await response.json();
    const example = plan.repositories.find((r: { repo: string }) => r.repo === FIXTURE.repos[0]);
    if (cursor === 'second') await gate;
    await route.fulfill({
      json: {
        ...plan,
        repositories: [{ ...example, repo: `acme/batch-${cursor || 'first'}` }],
        total_repos: 3,
        next_cursor: cursor === '' ? 'second' : cursor === 'second' ? 'third' : '',
      },
    });
  });
  await walkTo(page, 1);
  await page.getByRole('button', { name: 'Pause scan' }).click();
  releaseBatch();
  await expect(page.getByText('Repository scan paused')).toBeVisible();
  await expect(page.getByText(/Unchecked repositories will not be included/)).toBeVisible();
  expect(cursors).toEqual(['', 'second']);
  await expect(page.getByRole('button', { name: 'Continue with 2 selected' })).toBeEnabled();
  await page.getByRole('button', { name: 'Scan remaining repositories' }).click();
  await expect(page.getByText('Repository scan complete')).toBeVisible();
  await expect(
    page.getByRole('checkbox', { name: 'acme/batch-third', exact: false }),
  ).toBeChecked();
});

test('the review step offers the badge, ticked, and shows the line it would add', async ({
  page,
}) => {
  await walkTo(page, 4);

  // On by default: the badge is how the people who read a README find out
  // what runs its CI. It is a checkbox because the README is somebody else's.
  const box = page.getByRole('checkbox', { name: 'Add the badge to each README' });
  await expect(box).toBeChecked();

  // The exact line, not a description of one, beside the badge it renders.
  const preview = page.getByRole('img', { name: 'CI has the Zoomies' });
  await expect(preview).toBeVisible();
  // Visible is not loaded: an <img> with width and height attributes takes up
  // the same box whether its source resolved or a broken-image icon sits in
  // it, so the preview of a badge that 404s would pass the assertion above.
  // This one is about the asset actually being served with the UI.
  await expect
    .poll(() => preview.evaluate((el: HTMLImageElement) => el.naturalWidth))
    .toBeGreaterThan(0);
  await expect(
    page.getByText('[![CI has the Zoomies](https://zoomies.sh/badge.svg)'),
  ).toBeVisible();

  // Declining is one click, and the preview stays so it can be reconsidered.
  await box.uncheck();
  await expect(box).not.toBeChecked();
  await expect(preview).toBeVisible();
});

import { expect, test } from '@playwright/test';
import { browserOverride, clearStoredPreferences, goto, reload } from './support/fixtures';

test.use(browserOverride);

const pages = [
  ['/runners', 'Runners'],
  ['/workflows', 'Workflows'],
  ['/queue', 'Queue'],
] as const;

test('Appearance persists the style across runners, workflows and queue', async ({ page }) => {
  await goto(page, '/settings/appearance', 'Appearance');
  const choice = page.getByRole('group', { name: 'Zoomies vocabulary' });
  // A browser with nothing saved -- a new install, or preferences cleared --
  // starts on plain words and icons.
  await expect(choice.getByRole('button', { name: /^Off/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
  await choice.getByRole('button', { name: /^Standard/ }).click();
  await page.reload();
  await expect(choice.getByRole('button', { name: /^Standard/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  );

  for (const [path, title] of pages) {
    await goto(page, path, title);
    const marks = page.locator('svg[data-style="standard"]');
    await expect(marks.first()).toBeVisible();
    const cues: Record<string, string> = {
      busy: 'work',
      zoomies: 'boost',
      maximum_zoomies: 'maximum',
      idle: 'ready',
      provisioning: 'search',
      registering: 'hello',
      throttled: 'pause',
      draining: 'rest',
      failed: 'error',
      removed: 'gone',
      unknown: 'question',
    };
    for (const mark of await marks.all()) {
      const state = await mark.getAttribute('data-state');
      await expect(mark).toHaveAttribute('data-cue', cues[state ?? 'unknown']!);
    }
    await expect(page.locator('svg[data-style="cute"]')).toHaveCount(0);
    // A mark holds Lucide icons as nested SVGs, so count the marks, not every svg.
    if (path === '/workflows')
      await expect(
        page.locator('.avatars.pack').first().locator('svg[data-style="standard"]'),
      ).toHaveCount(3);
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await expect
      .poll(() =>
        marks.evaluateAll((svgs) =>
          svgs.reduce((n, svg) => n + svg.getAnimations({ subtree: true }).length, 0),
        ),
      )
      .toBe(0);
  }

  await goto(page, '/settings/appearance', 'Appearance');
  await choice.getByRole('button', { name: /^Cute/ }).click();
  await goto(page, '/runners', 'Runners');
  await expect(page.locator('svg[data-style="cute"]').first()).toBeVisible();
  await expect(page.locator('svg[data-style="standard"]')).toHaveCount(0);

  await goto(page, '/settings/appearance', 'Appearance');
  await choice.getByRole('button', { name: /^Off/ }).click();
  for (const [path, title] of pages) {
    await goto(page, path, title);
    await expect(page.locator('svg[data-style]')).toHaveCount(0);
    await expect(
      page.locator('.runner-status .standard-icon, .activity-status .standard-icon').first(),
    ).toBeVisible();
  }
});

test('a saved vocabulary opt-out stays off after the upgrade', async ({ page }) => {
  await page.addInitScript(() => {
    if (!localStorage.getItem('zoomies.prefs'))
      localStorage.setItem('zoomies.prefs', JSON.stringify({ quirkyStatus: false }));
  });
  await goto(page, '/settings/appearance', 'Appearance');
  await expect(
    page.getByRole('group', { name: 'Zoomies vocabulary' }).getByRole('button', { name: /^Off/ }),
  ).toHaveAttribute('aria-pressed', 'true');
  await goto(page, '/runners', 'Runners');
  await expect(page.locator('svg[data-style]')).toHaveCount(0);
});

test('clearing saved preferences puts the style back to Off', async ({ page }) => {
  await goto(page, '/settings/appearance', 'Appearance');
  const choice = page.getByRole('group', { name: 'Zoomies vocabulary' });
  await choice.getByRole('button', { name: /^Cute/ }).click();
  await reload(page, 'Appearance');
  await expect(choice.getByRole('button', { name: /^Cute/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  );

  await clearStoredPreferences(page);
  await reload(page, 'Appearance');
  await expect(choice.getByRole('button', { name: /^Off/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
  await goto(page, '/runners', 'Runners');
  await expect(page.locator('svg[data-style]')).toHaveCount(0);
});

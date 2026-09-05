/**
 * The scrollbar is a control an operator has to be able to find and take hold
 * of, which docs/ui-guidelines.md section 1.8 fixes at a token's width on a
 * pointer device and leaves to the browser's overlay bar on a phone.
 *
 * A file of its own because headless Chromium hides every scrollbar unless
 * told not to, and a hidden scrollbar measures exactly like a thin one --
 * nothing -- so the browser here is launched with that flag dropped, and a
 * launch option cannot change inside a describe block.
 */
import { expect, test } from '@playwright/test';
import { browserOverride, goto, grid, waitForRows } from './support/fixtures';

test.use({
  launchOptions: {
    ...('launchOptions' in browserOverride ? browserOverride.launchOptions : {}),
    ignoreDefaultArgs: ['--hide-scrollbars'],
  },
});

test('the scrollbar is wide enough to take hold of, and out of the way on a phone', async ({
  page,
  isMobile,
}) => {
  // Jobs has more rows than its grid shows at once, so the frame around the
  // table scrolls. Reached structurally: nothing in the accessibility tree
  // names a scroll frame, and the grid is the element inside it.
  await goto(page, '/jobs', 'Jobs');
  const jobs = grid(page, 'Jobs');
  await waitForRows(jobs);

  const measured = await jobs.evaluate((table) => {
    const frame = table.parentElement as HTMLElement;
    const style = getComputedStyle(frame);
    const borders = parseFloat(style.borderLeftWidth) + parseFloat(style.borderRightWidth);
    return {
      overflows: frame.scrollHeight > frame.clientHeight,
      // What the scrollbar takes out of the frame's width, which is its whole
      // hit target -- or nothing, for an overlay scrollbar.
      gutter: frame.offsetWidth - frame.clientWidth - borders,
      size: parseFloat(
        getComputedStyle(document.documentElement).getPropertyValue('--z-scrollbar-size'),
      ),
    };
  });
  expect(measured.overflows, 'the jobs grid has something to scroll').toBe(true);

  if (isMobile) {
    // A finger scrolls the content, never the bar, so the phone keeps the
    // browser's overlay scrollbar and no column is reserved for one.
    expect(measured.gutter, 'no column is reserved on a phone').toBe(0);
  } else {
    // The column is drawn at the token's width, and all of it is the target.
    // This is the regression guard: set `scrollbar-width` beside the
    // `::-webkit-scrollbar` rules and Chromium ignores them, draws its own
    // thin bar, and this stops being equal.
    expect(measured.gutter, 'the scrollbar column is the token width').toBe(measured.size);
  }
});

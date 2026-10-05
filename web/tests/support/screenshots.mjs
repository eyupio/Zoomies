// Captures the screenshots docs/ and README.md embed.
//
// They are taken from the real binary, not a mock or a design file: the same
// controller the Playwright suite drives, with the demo fleet seeded,
// authentication on and an administrator signed in. A screenshot therefore
// cannot show a page the product does not have, and refreshing them after a
// UI change is one command:
//
//   make screenshots                                 writes docs/screenshots/*.webp
//   node tests/support/screenshots.mjs DIR           writes them somewhere else
//   node tests/support/screenshots.mjs DIR hosts,usage  only the shots named
//
// Each theme gets a controller of its own. The fixture is placed relative to
// the moment it is seeded and the scheduler starts working on it at once --
// idle runners drain, silent hosts turn unhealthy -- so every page is captured
// in the first seconds of a controller's life, while the fleet still looks like
// the morning the seed describes.
//
// The files are lossless WebP at under half the size of the PNGs Playwright
// takes, so a page weighs about as much as a photograph rather than a small
// binary. The encoding is Pillow's (`pip install pillow`): the browser can write
// lossless WebP itself but compresses it six times worse, and the site already
// needs a Python environment to build.

import { spawn, spawnSync } from 'node:child_process';
import { controllerEnv } from './controller.mjs';
import { createHmac, randomUUID } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { chromium, devices } from '@playwright/test';

const PORT = 8097;
const root = resolve(import.meta.dirname, '..', '..', '..');
const binary = join(root, 'zoomies');
const outDir = resolve(process.argv[2] ?? join(root, 'docs', 'screenshots'));
/** Shot names to capture, for a change to one page; empty means every shot. */
const only = (process.argv[3] ?? '').split(',').filter(Boolean);

/** The administrator every shot is signed in as. The database is thrown away. */
const ADMIN = { username: 'alice', password: 'screenshots-only-not-a-secret' };

/** A common laptop, at retina density so the text survives being scaled down. */
const DESKTOP = { width: 1440, height: 900 };
const SCALE = 2;
/** Long enough for the event stream to fill the panels the first fetch does not. */
const SETTLE_MS = 750;

/** Identifiers from internal/controller/seed.go, so a shot can open a detail page. */
const FIXTURE = {
  linuxPool: 'pool_demolinux',
  busyRunner: 'run_demo00',
  // The machine still on its way, rather than the one that finished: a
  // timeline whose last row is still counting is what that page is for.
  buildingMachine: 'mach_demo02',
  installationID: 7654321,
  org: 'acme',
  webhookSecret: 'demo-webhook-secret',
};

/**
 * What is captured. `heading` is the <h1> that proves the route rendered;
 * `prepare` puts the page into the state worth showing after it has settled;
 * `clip` names a region to photograph instead of the whole page; `device`
 * gives the shot a browser context of its own -- a phone, or a desktop whose
 * `prepare` changes a per-operator preference that must not follow the shots
 * captured after it; `signedOut` keeps the session cookie out of that
 * context, for the one page that is only shown to nobody.
 */
const SHOTS = [
  { name: 'overview', path: '/', heading: 'Overview' },
  {
    name: 'activity',
    path: '/',
    heading: 'Overview',
    // Its own context: this shot widens the matrix, and the range is a
    // per-operator preference, so on the shared one the two Overview shots
    // taken after it would be photographed showing a year.
    device: { viewport: DESKTOP, deviceScaleFactor: SCALE },
    async prepare(page) {
      const matrix = page.getByRole('region', { name: 'Activity matrix', exact: true });
      // The matrix opens on today, hour by hour, and this shot is of a day
      // and the hours inside it -- so it takes the range that draws days.
      await matrix.getByRole('button', { name: /^1y/ }).click();
      // A square named for a weekday is the year's grid having arrived; the
      // hourly one it replaces names a time of day.
      await matrix
        .getByRole('gridcell', { name: /^\w+day \d/ })
        .first()
        .waitFor();
      // The newest square with jobs in it is the one tab stop; selecting it
      // opens the day under the grid, hour by hour.
      await matrix.locator('[role="gridcell"][tabindex="0"]').click();
      await matrix.getByRole('img', { name: /^Hour by hour:/ }).waitFor();
      // The pointer is still on the square, and its tooltip would cover the
      // header; the detail under the grid is what this shot is of.
      await page.mouse.move(0, 0);
    },
    clip: 'Activity matrix',
  },
  {
    name: 'problems',
    path: '/',
    heading: 'Overview',
    async prepare(page) {
      await page.getByRole('button', { name: /^Problems\./ }).click();
      await page.getByRole('dialog', { name: 'Problems' }).waitFor();
    },
  },
  {
    name: 'command-palette',
    path: '/',
    heading: 'Overview',
    async prepare(page) {
      await page.keyboard.press('Control+k');
      const palette = page.getByRole('dialog', { name: 'Command palette' });
      await palette.waitFor();
      await palette.getByRole('combobox').fill('demo');
    },
  },
  { name: 'pools', path: '/pools', heading: 'Pools' },
  { name: 'pool', path: `/pools/${FIXTURE.linuxPool}` },
  { name: 'runners', path: '/runners', heading: 'Runners' },
  {
    name: 'runner-history',
    path: '/runners',
    heading: 'Runners',
    async prepare(page) {
      await page
        .getByText('Explore fleet activity over the last 24 hours', { exact: true })
        .click();
      await page
        .getByRole('region', { name: 'Fleet activity', exact: true })
        .scrollIntoViewIfNeeded();
      // Scrolling brings a legend row under the pointer that opened the
      // panel, and a row under the pointer singles its figure out and steps
      // the others back. The shot is of the chart at rest.
      await page.mouse.move(0, 0);
    },
  },
  { name: 'queue', path: '/queue', heading: 'Queue' },
  { name: 'runner', path: `/runners/${FIXTURE.busyRunner}` },
  { name: 'jobs', path: '/jobs', heading: 'Jobs' },
  {
    name: 'job',
    path: '/jobs?failed=true',
    heading: 'Jobs',
    async prepare(page) {
      // A job that failed on a step of its own, rather than the one whose
      // runner died under it: the drawer names the step and links its log.
      const rows = page.getByRole('grid', { name: 'Jobs' }).locator('tbody tr[data-row]');
      await rows
        .filter({ hasText: 'Failure' })
        .filter({ hasNotText: 'Runner lost' })
        .first()
        .click();
      await page.getByRole('dialog').waitFor();
    },
  },
  { name: 'usage', path: '/usage', heading: 'Usage' },
  { name: 'hosts', path: '/hosts', heading: 'Hosts' },
  { name: 'providers', path: '/providers', heading: 'Providers' },
  { name: 'machine', path: `/machines/${FIXTURE.buildingMachine}` },
  { name: 'installations', path: '/installations', heading: 'Installations' },
  {
    name: 'migrate',
    path: '/migrate',
    heading: 'Migrate repositories',
    async prepare(page) {
      // The review step: the exact diff, and the jobs it will not touch.
      await page.getByRole('radio', { name: 'acme', exact: false }).waitFor();
      for (let step = 0; step < 4; step++) {
        await page.getByRole('button', { name: 'Next' }).click();
      }
      await page.getByRole('heading', { level: 2, name: 'Review' }).waitFor();
    },
  },
  {
    name: 'ai-context',
    path: '/ai-context',
    heading: 'AI Context',
    async prepare(page) {
      // Two saved drafts, so the page shows what an installation's list looks
      // like before anything is published. Created through the same API the
      // wizard uses; a repeat in the second colour scheme is a fresh database.
      await seedAIContextDrafts(page, ['acme/widgets', 'acme/api']);
      await page.reload({ waitUntil: 'domcontentloaded' });
      await page.getByRole('heading', { level: 2, name: 'acme/widgets' }).waitFor();
    },
  },
  {
    name: 'ai-context-wizard',
    path: '/ai-context/setup',
    heading: 'Enable repositories',
    async prepare(page) {
      // The review step: what will be saved, and every path setup will touch.
      await page.getByLabel('GitHub installation').selectOption({ label: FIXTURE.org });
      await page.getByRole('checkbox', { name: 'acme/site', exact: true }).check();
      await page.getByRole('checkbox', { name: 'acme/docs', exact: true }).check();
      for (let step = 0; step < 5; step++) {
        await page.getByRole('button', { name: 'Next', exact: true }).click();
      }
      await page.getByRole('heading', { name: 'Repository setup paths' }).waitFor();
    },
  },
  {
    name: 'ai-context-owners',
    path: '/ai-context',
    heading: 'AI Context',
    async prepare(page) {
      // Somebody to delegate to who is not an administrator.
      const made = await page.request.post('/api/v1/users', {
        headers: { Origin: new URL(page.url()).origin },
        data: {
          username: 'bob',
          display_name: 'Bob Chen',
          password: 'screenshots-only-not-a-secret',
          role: 'viewer',
        },
      });
      if (made.status() !== 201 && made.status() !== 409) {
        throw new Error(`creating the owner returned ${made.status()}: ${await made.text()}`);
      }
      await page.getByText('Installation owners', { exact: true }).click();
      await page.getByRole('button', { name: `Owners of ${FIXTURE.org}` }).click();
      const dialog = page.getByRole('dialog', { name: 'Installation owners' });
      await dialog.getByRole('checkbox', { name: /Bob Chen/ }).check();
    },
  },
  { name: 'audit', path: '/audit', heading: 'Audit' },
  // The section's rail beside the page that has a table on it.
  { name: 'settings', path: '/settings/users', heading: 'Users' },
  // The two wizards. The docs discuss both at length and photographed
  // neither, so the only way to know what "the labels step previews the
  // runs-on line" looks like was to install Zoomies and find out.
  {
    name: 'pool-wizard',
    path: '/pools/new',
    heading: 'Create a pool',
    async prepare(page) {
      // The labels step, which is the one worth showing: it previews the
      // runs-on line the labels produce.
      await page.getByRole('button', { name: 'Next' }).click();
      await page.getByRole('textbox', { name: 'Labels' }).waitFor();
    },
  },
  {
    name: 'add-host',
    path: '/hosts/new',
    heading: 'Add a host',
  },
  {
    // The Size step of the pool editor with the scratch space in memory turned
    // on: the one place that says what the setting costs. It is a fixed size
    // because only a typed limit can be compared with what the folder may take,
    // and 6 GB with the default 4 GB work folder is the case where the editor has
    // something to propose.
    name: 'pool-size-memory',
    path: '/pools/new',
    heading: 'Create a pool',
    async prepare(page) {
      await page.getByRole('radio', { name: 'Advanced' }).check();
      await page.getByRole('button', { name: 'Next' }).click();
      await page.getByRole('textbox', { name: 'Pool name' }).fill('zoomies-6gb-ubuntu-2404');
      await page.getByRole('button', { name: 'Next' }).click();
      const labels = page.getByRole('textbox', { name: 'Labels' });
      await labels.fill('zoomies-6gb-ubuntu-2404');
      await page.keyboard.press('Enter');
      for (let step = 0; step < 3; step++) {
        await page.getByRole('button', { name: 'Next' }).click();
      }
      await page.getByRole('heading', { level: 2, name: 'Size' }).waitFor();
      await page.getByRole('radio', { name: 'A fixed size on every host' }).check();
      const memory = page.getByRole('textbox', { name: 'Memory per runner', exact: true });
      await memory.fill('6g');
      await memory.press('Enter');
      // Auto is the default placement and is what the shot shows: on a 6 GB runner
      // the work folder would come out too small to be useful, so it stays on disk
      // there rather than failing jobs, and the editor says so.
      await page.getByRole('checkbox', { name: 'Keep the work folder in memory' }).check();
      await page.getByRole('radio', { name: /^Auto/ }).waitFor();
      await page
        .getByText('Scratch space in memory')
        .evaluate((el) => el.scrollIntoView({ block: 'start' }));
    },
  },
  {
    // How a slot is divided between the runner and its Docker sidecar: CPU and
    // memory are two shares, the presets are priced on the fleet's hosts, and one is
    // already chosen. Nothing is typed.
    name: 'pool-size-split',
    path: '/pools/new',
    heading: 'Create a pool',
    async prepare(page) {
      await page.getByRole('radio', { name: 'Advanced' }).check();
      await page.getByRole('button', { name: 'Next' }).click();
      await page.getByRole('textbox', { name: 'Pool name' }).fill('zoomies-dind-builds');
      await page.getByRole('button', { name: 'Next' }).click();
      const labels = page.getByRole('textbox', { name: 'Labels' });
      await labels.fill('zoomies-dind-builds');
      await page.keyboard.press('Enter');
      for (let step = 0; step < 2; step++) {
        await page.getByRole('button', { name: 'Next' }).click();
      }
      await page.getByRole('radio', { name: 'Docker in Docker' }).check();
      await page.getByRole('button', { name: 'Next' }).click();
      await page.getByRole('heading', { level: 2, name: 'Size' }).waitFor();
      const split = page.getByTestId('pool-split');
      await split.waitFor();
      await split.getByRole('radio', { checked: true }).waitFor();
      await split.evaluate((el) => el.scrollIntoView({ block: 'start' }));
    },
  },
  {
    // The host's runner sizes, scrolled to the in-memory folders: the host's
    // owner has the last word on what a pool may ask of the machine. Nothing is
    // saved; a work folder size and a ceiling are typed so the section is shown in use.
    name: 'host-runner-sizes',
    path: '/hosts',
    heading: 'Hosts',
    async prepare(page) {
      await page
        .getByRole('article', { name: 'demo-builder-1', exact: true })
        .getByRole('button', { name: /Actions for/ })
        .click();
      await page.getByRole('menuitem', { name: 'Set runner sizes' }).click();
      const dialog = page.getByRole('dialog', { name: 'Runner sizes on demo-builder-1' });
      await dialog.waitFor();
      await dialog.getByRole('textbox', { name: 'Work folder size (MB)' }).fill('16384');
      await dialog.getByRole('textbox', { name: 'Largest folder (MB)' }).fill('20000');
      await dialog
        .getByText('In-memory folders')
        .first()
        .evaluate((el) => el.scrollIntoView({ block: 'center' }));
    },
  },
  {
    // The first step, which opens on what the driver needs you to have done
    // before the form is any use -- the token, the template, the block of
    // VMIDs, each with the commands that make it. Nothing is typed: a
    // screenshot of a half-filled form teaches the answers rather than the
    // questions.
    name: 'provider-wizard',
    path: '/providers/new',
    heading: 'Add a provider',
  },
  {
    name: 'private-host',
    path: '/hosts/new',
    heading: 'Add a host',
    async prepare(page) {
      await page.getByRole('radio', { name: /Private connection/ }).check();
    },
  },
  // Sign-in, as anyone arriving at the address meets it. The shot has a
  // context of its own that is never handed the administrator's cookie, so the
  // controller that serves every other page answers this one signed out.
  {
    name: 'sign-in',
    path: '/login',
    heading: 'Sign in',
    signedOut: true,
    device: { viewport: DESKTOP, deviceScaleFactor: SCALE },
  },
  // Read-only monitoring on a phone is a stated requirement, so it is shown.
  { name: 'overview-phone', path: '/', heading: 'Overview', device: devices['Pixel 7'] },
];

/**
 * A browser override for sandboxes where Playwright's own Chromium download is
 * absent and a compatible build sits somewhere else. Same variable as the
 * suite's fixtures.ts.
 */
const launchOptions = process.env.PLAYWRIGHT_CHROMIUM
  ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM }
  : {};

/**
 * Saved AI Context drafts for the named demo repositories, through the API the
 * wizard calls. A draft that already exists answers 409 and is left alone.
 */
async function seedAIContextDrafts(page, names) {
  const { items } = await (await page.request.get('/api/v1/ai-context/installations')).json();
  const installation = items[0].id;
  const discovery = await (
    await page.request.get(`/api/v1/ai-context/discovery?installation_id=${installation}`)
  ).json();
  for (const name of names) {
    const repository = discovery.repositories.find((r) => r.full_name === name);
    const res = await page.request.post('/api/v1/ai-context/repositories', {
      // A cookie-authenticated write has to show it came from the UI.
      headers: { Origin: new URL(page.url()).origin },
      data: { installation_id: installation, repository_id: repository.id },
    });
    if (res.status() !== 201 && res.status() !== 409) {
      throw new Error(`creating the ${name} draft returned ${res.status()}: ${await res.text()}`);
    }
  }
}

/** Boot a seeded controller with authentication on, and wait until it answers. */
async function bootController(dir) {
  const child = spawn(binary, ['controller'], {
    // stdout is piped rather than ignored because the setup token is printed
    // there and nowhere else: the bootstrap route asks for it as proof that
    // whoever creates the first administrator can read the controller's log,
    // and this harness is in the same position as an operator. It is echoed on,
    // so a controller that fails to boot is still readable.
    stdio: ['ignore', 'pipe', 'inherit'],
    env: {
      ...process.env,
      ...controllerEnv(dir, PORT),
      // Configured the way a finished install is, so the problems drawer
      // shows the fleet's problems and not this harness's: an external URL
      // (loopback, so the session cookie is not marked Secure and a plain-http
      // page can hold it) and the poller left on. The poller skips the demo
      // installation, so nothing reaches for GitHub.
      ZOOMIES_EXTERNAL_URL: `http://127.0.0.1:${PORT}`,
      ZOOMIES_POLL_FALLBACK: 'true',
      ZOOMIES_SEED_DEMO: 'true',
    },
  });
  // Scoped to this controller, not to the module: each colour scheme gets a
  // fresh database and therefore a fresh token, and the second boot carrying
  // the first one over is a 422 that reads like a broken form.
  let setupToken = '';
  child.stdout.on('data', (chunk) => {
    process.stdout.write(chunk);
    if (setupToken !== '') return;
    const match = /setup token\s+(\S+)/.exec(String(chunk));
    if (match) setupToken = match[1];
  });
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) {
      throw new Error(`the controller exited with ${child.exitCode} before it was ready`);
    }
    try {
      const res = await fetch(`http://127.0.0.1:${PORT}/healthz`);
      // Answering /healthz does not mean the token line has been flushed, so
      // both are waited for: bootstrapping without one is a 422 that reads
      // like a bug in the form rather than a race in this script.
      if (res.ok && setupToken !== '') return { child, setupToken };
    } catch {
      /* not listening yet */
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  child.kill('SIGKILL');
  throw new Error(
    setupToken === ''
      ? 'the controller never printed a setup token'
      : 'the controller did not answer /healthz within 30s',
  );
}

function stopController(child) {
  return new Promise((done) => {
    if (child.exitCode !== null) return done();
    const hard = setTimeout(() => child.kill('SIGKILL'), 5_000);
    child.once('exit', () => {
      clearTimeout(hard);
      done();
    });
    child.kill('SIGTERM');
  });
}

/**
 * Deliver GitHub's ping, signed with the demo installation's secret.
 *
 * A verified delivery is what tells the controller that webhooks reach it; until
 * one has, every page carries a warning that scaling is running on the poller,
 * which is true of this harness and not of the fleet the screenshots show.
 */
async function pingWebhook(baseURL) {
  const body = JSON.stringify({
    zen: 'Keep it logically awesome.',
    hook_id: 1,
    organization: { login: FIXTURE.org },
    installation: { id: FIXTURE.installationID },
  });
  const signature = createHmac('sha256', FIXTURE.webhookSecret).update(body).digest('hex');
  const res = await fetch(`${baseURL}/webhooks/github`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'x-github-event': 'ping',
      'x-github-delivery': randomUUID(),
      'x-hub-signature-256': `sha256=${signature}`,
    },
    body,
  });
  if (!res.ok) {
    throw new Error(`the ping was not accepted: ${res.status} ${await res.text()}`);
  }
}

/** Encode every PNG in `pngDir` as lossless WebP in `outDir`, with Pillow. */
function encodeWebp(pngDir) {
  const script = [
    'import pathlib, sys',
    'from PIL import Image',
    'src, dst = map(pathlib.Path, sys.argv[1:3])',
    'for png in sorted(src.glob("*.png")):',
    '    out = dst / (png.stem + ".webp")',
    '    Image.open(png).convert("RGB").save(out, lossless=True, quality=100, method=6)',
    '    print(f"  {out.name}  {out.stat().st_size // 1024} KB")',
  ].join('\n');
  const run = spawnSync('python3', ['-u', '-c', script, pngDir, outDir], { stdio: 'inherit' });
  if (run.error || run.status !== 0) {
    throw new Error(
      'encoding the screenshots needs Python 3 with Pillow: pip install pillow\n' +
        `(python3 ${run.error ? `could not start: ${run.error.message}` : `exited ${run.status}`})`,
    );
  }
}

/** Wait for the page itself, never for the network: the event stream never idles. */
async function settle(page, shot) {
  const heading = shot.heading
    ? page.getByRole('heading', { level: 1, name: shot.heading, exact: true })
    : page.getByRole('heading', { level: 1 }).first();
  await heading.waitFor();
  await page.evaluate(() => document.fonts.ready);
  // The table kind of grid, whose rows arrive after the page does. The
  // activity matrix is a grid too, and has no rows to wait for.
  const grid = page.locator('table[role="grid"]').first();
  if (await grid.count()) {
    await grid.locator('tbody tr[data-row]').first().waitFor();
  }
  await page.waitForTimeout(SETTLE_MS);
}

async function capture(browser, scheme, pngDir) {
  const dir = mkdtempSync(join(tmpdir(), 'zoomies-screenshots-'));
  const { child, setupToken } = await bootController(dir);
  const baseURL = `http://127.0.0.1:${PORT}`;
  // en-GB, and a fixed one. Native date inputs render in the browser's locale,
  // and a runner with none set takes the machine's -- so the shipped
  // documentation showed `mm/dd/yyyy` placeholders to a project whose prose is
  // British throughout. The timezone is pinned for the same reason: a
  // screenshot of "3 minutes ago" should not depend on where CI is.
  const common = {
    baseURL,
    colorScheme: scheme,
    reducedMotion: 'reduce',
    locale: 'en-GB',
    timezoneId: 'Europe/London',
  };
  try {
    const desktop = await browser.newContext({
      ...common,
      viewport: DESKTOP,
      deviceScaleFactor: SCALE,
    });
    // The first administrator, created the way the first-run form does it.
    // The 201 sets the session cookie on this context.
    const created = await desktop.request.post('/api/v1/auth/bootstrap', {
      data: { ...ADMIN, setup_token: setupToken },
    });
    if (created.status() !== 201) {
      throw new Error(`bootstrap returned ${created.status()}: ${await created.text()}`);
    }
    const cookies = await desktop.cookies();
    await pingWebhook(baseURL);

    for (const shot of SHOTS) {
      if (only.length > 0 && !only.includes(shot.name)) continue;
      console.log(`  capturing ${shot.name}-${scheme}`);
      let context = desktop;
      if (shot.device) {
        context = await browser.newContext({ ...shot.device, ...common });
        if (!shot.signedOut) await context.addCookies(cookies);
      }
      const page = await context.newPage();
      await page.goto(shot.path, { waitUntil: 'domcontentloaded' });
      await settle(page, shot);
      if (shot.prepare) {
        await shot.prepare(page);
        await page.waitForTimeout(SETTLE_MS / 2);
      }
      // A shot may be one panel rather than the page, for the docs that
      // discuss that panel on its own.
      const target = shot.clip ? page.getByRole('region', { name: shot.clip, exact: true }) : page;
      const png = await target.screenshot({ animations: 'disabled', caret: 'hide' });
      writeFileSync(join(pngDir, `${shot.name}-${scheme}.png`), png);
      await page.close();
      if (shot.device) await context.close();
    }
    await desktop.close();
  } finally {
    await stopController(child);
    rmSync(dir, { recursive: true, force: true });
  }
}

if (!existsSync(binary)) {
  console.error(`zoomies binary not found at ${binary}.\nBuild it first:  make build`);
  process.exit(1);
}
mkdirSync(outDir, { recursive: true });

const pngDir = mkdtempSync(join(tmpdir(), 'zoomies-screenshots-png-'));
const browser = await chromium.launch(launchOptions);
try {
  for (const scheme of ['dark', 'light']) {
    console.log(`capturing ${scheme}`);
    await capture(browser, scheme, pngDir);
  }
  console.log(`encoding into ${outDir}`);
  encodeWebp(pngDir);
} finally {
  await browser.close();
  rmSync(pngDir, { recursive: true, force: true });
}
console.log(`wrote ${(only.length > 0 ? only.length : SHOTS.length) * 2} screenshots to ${outDir}`);

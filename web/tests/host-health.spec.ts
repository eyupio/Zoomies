import { expect, test } from '@playwright/test';
import { browserOverride, goto, plantMarker, expectNoReload } from './support/fixtures';
test.use(browserOverride);
test('host OS health updates live and stays read-only on the detail page', async ({ page }) => {
  await goto(page, '/hosts/new', 'Add a host');
  await page.getByRole('button', { name: 'Get the command' }).click();
  const command = await page
    .getByRole('region', { name: 'Run this on the new host' })
    .locator('pre', { hasText: 'zoomies.sh/install.sh' })
    .innerText();
  const token = /--join-token\s+['"]?(zoojoin_[^'"\s]+)/.exec(command)?.[1];
  const name = `health-host-${Date.now()}`;
  const join = await page.request.post('/api/v1/agent/join', {
    data: {
      protocol_version: 1,
      join_token: token,
      name,
      capacity: 1,
      os: 'linux',
      arch: 'amd64',
      version: 'dev',
      backends: [{ kind: 'docker', available: true }],
    },
  });
  expect(join.ok()).toBeTruthy();
  const credentials = (await join.json()) as { host_id: string; agent_token: string };
  const beat = async (warn: boolean, reboot = false) => {
    const response = await page.request.post('/api/v1/agent/heartbeat', {
      headers: { Authorization: `Bearer ${credentials.agent_token}` },
      data: {
        protocol_version: 1,
        doctor: {
          checked_at: new Date().toISOString(),
          os: 'linux',
          distro: 'ubuntu 24.04',
          container: false,
          reboot_pending: reboot,
          results: [
            {
              id: 'disk.space',
              title: 'Work directory free space',
              tier: 'safe',
              status: warn ? 'warn' : 'ok',
              current: warn ? '2% free' : '45% free',
              recommended: '10% and 10 GiB free',
              rationale: 'Leave room for new builds.',
              actionable: false,
            },
          ],
        },
      },
    });
    expect(response.ok()).toBeTruthy();
  };
  try {
    await beat(true);
    await goto(page, '/hosts', 'Hosts');
    const card = page.getByRole('article', { name, exact: true });
    await expect(card.getByRole('link', { name: `Host health for ${name}` })).toContainText(
      '1 warning',
    );
    await plantMarker(page);
    await beat(false);
    await expect(card).toContainText('Health OK');
    await expectNoReload(page);
    await card.getByRole('link', { name: `Host health for ${name}` }).click();
    await expect(page.getByRole('heading', { level: 1, name, exact: true })).toBeVisible();
    await expect(page.getByRole('table')).toContainText('Leave room for new builds.');
    await expect(page.getByRole('button', { name: /apply|tune/i })).toHaveCount(0);
    await beat(false, true);
    await expect(page.getByText('Reboot pending', { exact: true })).toBeVisible();
    await expect(page.getByRole('main')).toContainText('Drain the host');
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(overflow).toBe(false);
  } finally {
    await page.request.delete(`/api/v1/hosts/${credentials.host_id}`);
  }
});

import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';

test('loads the embedded app with accessible core UI', async ({ page }) => {
  const response = await page.goto('/');

  expect(response?.status()).toBe(200);
  await expect(page).toHaveTitle('MaterialMind');
  await expect(page.getByRole('link', { name: 'MaterialMind home' })).toBeVisible();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'system');

  await page.evaluate(() => document.fonts.ready);
  const iconFont = await page
    .locator('.material-icons')
    .first()
    .evaluate((element) => {
      const style = getComputedStyle(element);
      const range = document.createRange();
      range.selectNodeContents(element);
      return {
        family: style.fontFamily,
        size: style.fontSize,
        textWidth: range.getBoundingClientRect().width,
      };
    });
  expect(iconFont.family).toContain('Material Symbols Rounded Variable');
  expect(iconFont.size).toBe('24px');
  expect(iconFont.textWidth).toBeLessThanOrEqual(26);

  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations, formatViolations(accessibility.violations)).toEqual([]);
});

test('manages retained data without responsive overflow', async ({ page }) => {
  const response = await page.goto('/settings/data');

  expect(response?.status()).toBe(200);
  await expect(page.getByRole('heading', { name: 'Data', level: 1 })).toBeVisible();
  await expect(page.getByLabel('Keep sessions')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Save' })).toBeDisabled();
  const download = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download backup' }).click();
  expect((await download).suggestedFilename()).toMatch(/^materialmind-\d{8}-\d{6}\.db$/);

  const overflow = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  expect(overflow.scrollWidth).toBeLessThanOrEqual(overflow.clientWidth);

  const accessibility = await new AxeBuilder({ page }).analyze();
  expect(accessibility.violations, formatViolations(accessibility.violations)).toEqual([]);
});

function formatViolations(violations: Array<{ id: string; help: string }>): string {
  return violations.map((violation) => `${violation.id}: ${violation.help}`).join('\n');
}

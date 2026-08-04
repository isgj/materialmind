import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: Boolean(process.env['CI']),
  retries: process.env['CI'] ? 2 : 0,
  reporter: process.env['CI'] ? [['github'], ['html', { open: 'never' }]] : 'line',
  workers: process.env['CI'] ? 1 : undefined,
  use: {
    baseURL: 'http://127.0.0.1:18080',
    launchOptions: process.env['CI'] ? {} : { executablePath: '/usr/bin/google-chrome' },
    trace: 'on-first-retry',
  },
  projects: [
    { name: 'desktop', use: { viewport: { width: 1920, height: 1080 } } },
    { name: 'mobile', use: { viewport: { width: 390, height: 844 } } },
  ],
  webServer: {
    command:
      'npm run build && ./dist/materialmind -addr 127.0.0.1:18080 -data-dir /tmp/materialmind-playwright -credential-store memory',
    cwd: '..',
    url: 'http://127.0.0.1:18080/api/health',
    reuseExistingServer: !process.env['CI'],
    timeout: 120_000,
  },
});

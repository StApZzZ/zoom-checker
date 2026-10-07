// Browser end-to-end tests against the real zoom-checker binary (bin/zoom-checker,
// built by `make build`). See e2e/README.md.

import { defineConfig, devices } from '@playwright/test'

import { APP_URL as AUTH_APP_URL } from './e2e/auth/stand'
import { AUTH_FILE } from './e2e/helpers/env'

const workers = Number(process.env.E2E_WORKERS || 2)

export default defineConfig({
  testDir: './e2e',
  globalSetup: './e2e/global-setup.ts',
  // Flakiness is a bug to fix, not to retry.
  retries: 0,
  workers,
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  timeout: 90_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : [['list']],
  use: {
    locale: 'ru-RU',
    timezoneId: 'Europe/Moscow',
    viewport: { width: 1366, height: 900 },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    actionTimeout: 15_000,
    navigationTimeout: 20_000,
  },
  projects: [
    { name: 'setup', testMatch: /auth\.setup\.ts/, use: { ...devices['Desktop Chrome'], viewport: { width: 1366, height: 900 } } },
    {
      name: 'chromium',
      testMatch: /.*\.spec\.ts/,
      testIgnore: /[\\/]e2e[\\/]auth[\\/]/,
      dependencies: ['setup'],
      use: { ...devices['Desktop Chrome'], viewport: { width: 1366, height: 900 }, storageState: AUTH_FILE },
    },
    {
      // Corporate login against deploy/dev/auth-stand (e2e/auth/README.md). Skipped
      // unless E2E_AUTH_STAND is set. Its own instance listens on the one address the
      // Keycloak realm accepts (localhost:8080), so its tests run one at a time.
      name: 'auth',
      testMatch: /[\\/]e2e[\\/]auth[\\/].*\.spec\.ts/,
      workers: 1,
      fullyParallel: false,
      use: { ...devices['Desktop Chrome'], viewport: { width: 1366, height: 900 }, baseURL: AUTH_APP_URL },
    },
  ],
})

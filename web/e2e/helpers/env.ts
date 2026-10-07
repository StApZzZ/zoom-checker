// Shared constants of the e2e run. globalSetup starts the server and exports
// its address through process.env, which Playwright passes on to the workers.

import path from 'node:path'

/** web/ */
export const WEB_DIR = path.resolve(import.meta.dirname, '..', '..')
/** Repository root. */
export const REPO_DIR = path.resolve(WEB_DIR, '..')

/** The binary under test (`make build`). */
export const BIN = process.env.E2E_BIN || path.join(REPO_DIR, 'bin', 'zoom-checker')

export const ADMIN_USERNAME = 'admin'
export const ADMIN_PASSWORD = process.env.E2E_ADMIN_PASSWORD || 'e2e-Admin-Pa55word'
export const WEBHOOK_SECRET = process.env.E2E_WEBHOOK_SECRET || 'e2e-zoom-secret-token'
export const TIMEZONE = 'Europe/Moscow'

/** Saved login of the admin (written by auth.setup.ts, used by every spec). */
export const AUTH_FILE = path.join(WEB_DIR, 'e2e', '.auth', 'admin.json')

/** Base URL of the shared server started by globalSetup. */
export function serverUrl(): string {
  const url = process.env.E2E_BASE_URL
  if (!url) throw new Error('E2E_BASE_URL is not set: run the suite through Playwright (globalSetup starts the server)')
  return url
}

/** Directory for per-run scratch files (dedicated servers, metrics). */
export function runDir(): string {
  const dir = process.env.E2E_RUN_DIR
  if (!dir) throw new Error('E2E_RUN_DIR is not set: run the suite through Playwright')
  return dir
}

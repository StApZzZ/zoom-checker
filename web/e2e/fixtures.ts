// The `test` every spec imports: baseURL points at the server globalSetup started.

import { test as base } from '@playwright/test'

import { serverUrl } from './helpers/env'

export const test = base.extend({
  // Playwright reads fixture dependencies from the first parameter's destructuring.
  // oxlint-disable-next-line no-empty-pattern
  baseURL: async ({}, provide) => {
    await provide(serverUrl())
  },
})

export { expect } from '@playwright/test'

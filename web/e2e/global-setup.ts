// Starts the real binary once for the whole run: fresh DATA_DIR, free port,
// known admin password and Zoom secret. E2E_DATABASE_URL switches it to
// PostgreSQL (the database must be empty). The returned function is the
// global teardown: it stops the server and writes the run's metrics.

import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import type { FullConfig } from '@playwright/test'

import { ADMIN_PASSWORD, BIN, WEB_DIR } from './helpers/env'
import { readMetrics } from './helpers/metrics'
import { startServer, stopPid } from './helpers/server'

export default async function globalSetup(_config: FullConfig) {
  const startedAt = Date.now()
  const base = process.env.E2E_TMP || os.tmpdir()
  const runDir = fs.mkdtempSync(path.join(base, 'zc-e2e-'))
  const dataDir = path.join(runDir, 'data')
  const databaseUrl = process.env.E2E_DATABASE_URL || ''

  const server = await startServer({
    dataDir,
    adminPassword: ADMIN_PASSWORD,
    databaseUrl,
    logFile: path.join(runDir, 'server.log'),
  })
  console.log(
    `[e2e] ${BIN} serving ${server.url} (pid ${server.pid}, db ${databaseUrl ? 'postgres' : 'sqlite'}, data ${dataDir})`,
  )

  process.env.E2E_BASE_URL = server.url
  process.env.E2E_RUN_DIR = runDir
  process.env.E2E_SERVER_PID = String(server.pid)
  process.env.E2E_SERVER_LOG = server.logFile

  return async () => {
    await stopPid(server.pid)
    const metrics = readMetrics(runDir)
    const totalMs = Date.now() - startedAt
    const summary = { total_suite_ms: totalMs, db: databaseUrl ? 'postgres' : 'sqlite', metrics }
    const outDir = path.join(WEB_DIR, 'test-results')
    fs.mkdirSync(outDir, { recursive: true })
    fs.writeFileSync(path.join(outDir, 'e2e-metrics.json'), JSON.stringify(summary, null, 2) + '\n')
    for (const m of metrics) console.log(`[e2e] metric ${m.name} = ${m.value} ${m.unit}`)
    console.log(`[e2e] total suite time ${(totalMs / 1000).toFixed(1)} s (metrics: test-results/e2e-metrics.json)`)
    // Keep the server log of a run for debugging; the database is throwaway.
    fs.copyFileSync(server.logFile, path.join(outDir, 'server.log'))
    if (!process.env.E2E_KEEP_DATA) fs.rmSync(runDir, { recursive: true, force: true })
    else console.log(`[e2e] kept ${runDir}`)
  }
}

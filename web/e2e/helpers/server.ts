// Starts and stops the real zoom-checker binary for the e2e suite.

import { spawn } from 'node:child_process'
import fs from 'node:fs'
import net from 'node:net'
import path from 'node:path'

import { BIN, TIMEZONE, WEBHOOK_SECRET } from './env'

export interface ServerOptions {
  dataDir: string
  /** Fixed port (a restart reuses it); a free one is picked when omitted. */
  port?: number
  /** null = start without ADMIN_PASSWORD (the server generates one). */
  adminPassword: string | null
  /** PostgreSQL URL; SQLite in dataDir when empty. */
  databaseUrl?: string
  /** Extra environment variables. */
  env?: Record<string, string>
  /** Server output is appended here (a file, not a pipe: the server must outlive the test worker). */
  logFile?: string
}

export interface RunningServer {
  url: string
  port: number
  dataDir: string
  pid: number
  logFile: string
  /** SIGTERM (graceful) or SIGKILL; resolves when the process is gone. */
  stop(signal?: NodeJS.Signals): Promise<void>
}

/** A TCP port that is free right now on 127.0.0.1. */
export async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer()
    srv.unref()
    srv.on('error', reject)
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address()
      const port = typeof addr === 'object' && addr ? addr.port : 0
      srv.close(() => resolve(port))
    })
  })
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

/** Waits until the process `pid` has exited (or the timeout passes). */
export async function waitForExit(pid: number, timeoutMs = 15_000): Promise<boolean> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (!alive(pid)) return true
    await new Promise((r) => setTimeout(r, 50))
  }
  return !alive(pid)
}

/** Stops a server started by startServer (also from another process, by pid). */
export async function stopPid(pid: number, signal: NodeJS.Signals = 'SIGTERM'): Promise<void> {
  if (!alive(pid)) return
  try {
    process.kill(pid, signal)
  } catch {
    return
  }
  if (!(await waitForExit(pid, signal === 'SIGKILL' ? 5_000 : 15_000))) {
    try {
      process.kill(pid, 'SIGKILL')
    } catch {
      // already gone
    }
    await waitForExit(pid, 5_000)
  }
}

/** Polls /healthz until it answers "ok". */
export async function waitHealthy(url: string, timeoutMs = 20_000, isAlive: () => boolean = () => true): Promise<void> {
  const deadline = Date.now() + timeoutMs
  let last = ''
  while (Date.now() < deadline) {
    if (!isAlive()) throw new Error(`server process exited before ${url}/healthz answered`)
    try {
      const res = await fetch(`${url}/healthz`, { signal: AbortSignal.timeout(2_000) })
      const text = await res.text()
      if (res.ok && text.trim() === 'ok') return
      last = `${res.status} ${text.slice(0, 100)}`
    } catch (err) {
      last = String(err)
    }
    await new Promise((r) => setTimeout(r, 100))
  }
  throw new Error(`${url}/healthz did not answer in ${timeoutMs} ms (last: ${last})`)
}

/** Environment of the server: only what the test sets (nothing leaks in from the shell). */
function serverEnv(opts: ServerOptions, port: number): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {
    PATH: process.env.PATH ?? '/usr/bin:/bin',
    HOME: process.env.HOME ?? opts.dataDir,
    DATA_DIR: opts.dataDir,
    LISTEN_ADDR: `127.0.0.1:${port}`,
    PUBLIC_URL: `http://127.0.0.1:${port}`,
    ZOOM_WEBHOOK_SECRET_TOKEN: WEBHOOK_SECRET,
    TIMEZONE,
    LOG_LEVEL: process.env.E2E_LOG_LEVEL || 'info',
    // A backup job per start is pointless for throwaway data.
    BACKUP_KEEP: '0',
    ...opts.env,
  }
  if (opts.adminPassword !== null) env.ADMIN_PASSWORD = opts.adminPassword
  if (opts.databaseUrl) env.DATABASE_URL = opts.databaseUrl
  return env
}

/** Starts `zoom-checker serve` and waits for /healthz. */
export async function startServer(opts: ServerOptions): Promise<RunningServer> {
  if (!fs.existsSync(BIN)) {
    throw new Error(`${BIN} not found: build it first with "make build" in the repository root`)
  }
  fs.mkdirSync(opts.dataDir, { recursive: true })
  const port = opts.port ?? (await freePort())
  const url = `http://127.0.0.1:${port}`
  const logFile = opts.logFile ?? path.join(opts.dataDir, 'server.log')
  const out = fs.openSync(logFile, 'a')
  const child = spawn(BIN, ['serve'], {
    env: serverEnv(opts, port),
    stdio: ['ignore', out, out],
    detached: false,
  })
  fs.closeSync(out)
  let exited = false
  child.on('exit', () => {
    exited = true
  })
  const pid = child.pid
  if (!pid) throw new Error(`could not start ${BIN}`)
  // The server may outlive this worker (globalSetup stops it by pid).
  child.unref()
  try {
    await waitHealthy(url, 30_000, () => !exited)
  } catch (err) {
    const tail = fs.existsSync(logFile) ? fs.readFileSync(logFile, 'utf8').split('\n').slice(-30).join('\n') : ''
    await stopPid(pid, 'SIGKILL')
    throw new Error(`${String(err)}\n--- server log (${logFile}) ---\n${tail}`)
  }
  return {
    url,
    port,
    dataDir: opts.dataDir,
    pid,
    logFile,
    stop: (signal: NodeJS.Signals = 'SIGTERM') => stopPid(pid, signal),
  }
}

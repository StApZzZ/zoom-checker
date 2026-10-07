// Client receive times of server snapshots, keyed by their `as_of` value.
//
// Live counters add "time since the snapshot arrived" (client clock only) to
// the server's total instead of comparing the server's as_of with the client
// clock, so a skewed client clock cannot make durations jump or go negative.
//
// A snapshot may also have been computed a while before it was sent — e.g. a
// projection stored at the last webhook and served minutes later, with as_of =
// its computation time. That age is measured on the *server* clock (HTTP `Date`
// header of the latest API response vs. as_of) and added once, so online
// students do not lose the minutes the snapshot spent in the database. Ages
// below STALE_AFTER_MS are latency and the 1 s resolution of `Date`; ignored.

const MAX_ENTRIES = 256
/** Snapshot ages below this are network latency / `Date` resolution, not staleness. */
export const STALE_AFTER_MS = 3_000
/** A server clock offset older than this is not trusted (the client clock may have been changed). */
const OFFSET_TTL_MS = 10 * 60_000

interface Receipt {
  /** Client time the snapshot arrived. */
  at: number
  /** How old the snapshot already was when it arrived (server clock), 0 when fresh or unknown. */
  ageMs: number
}

const receipts = new Map<string, Receipt>()
let serverOffset: { offsetMs: number; measuredAt: number } | null = null

/**
 * Records the server clock from an HTTP `Date` response header (called by the
 * API client for every response): offset = server time − client time.
 */
export function noteServerDate(dateHeader: string | null | undefined, clientNow: number = Date.now()): void {
  if (!dateHeader) return
  const serverMs = Date.parse(dateHeader)
  if (Number.isNaN(serverMs)) return
  // `Date` is truncated to whole seconds; +500 ms is the expected true value.
  serverOffset = { offsetMs: serverMs + 500 - clientNow, measuredAt: clientNow }
}

function snapshotAge(asOf: string, at: number): number {
  if (!serverOffset || Math.abs(at - serverOffset.measuredAt) > OFFSET_TTL_MS) return 0
  const asOfMs = Date.parse(asOf)
  if (Number.isNaN(asOfMs)) return 0
  const age = at + serverOffset.offsetMs - asOfMs
  return age > STALE_AFTER_MS ? age : 0
}

/**
 * Records when a snapshot computed at `asOf` reached the client. The first
 * receipt wins: re-delivery of the same snapshot must not restart the count.
 */
export function markReceived(asOf: string | null | undefined, at: number = Date.now()): void {
  if (!asOf || receipts.has(asOf)) return
  receipts.set(asOf, { at, ageMs: snapshotAge(asOf, at) })
  if (receipts.size > MAX_ENTRIES) {
    const oldest = receipts.keys().next().value
    if (oldest !== undefined) receipts.delete(oldest)
  }
}

/**
 * Client time (ms) that corresponds to the snapshot's `asOf`: its receive time,
 * minus how old it already was on arrival. Undefined when never received.
 */
export function receivedAt(asOf: string | null | undefined): number | undefined {
  const r = asOf ? receipts.get(asOf) : undefined
  return r ? r.at - r.ageMs : undefined
}

/** Clears all receipts and the server clock offset (tests). */
export function resetReceipts(): void {
  receipts.clear()
  serverOffset = null
}

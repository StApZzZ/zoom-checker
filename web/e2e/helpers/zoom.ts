// Zoom webhook builders and a sender that signs exactly like Zoom:
//   x-zm-request-timestamp = unix seconds
//   x-zm-signature         = "v0=" + hex(HMAC-SHA256(secret, "v0:{ts}:{rawBody}"))
// Payload shapes follow internal/zoom/testdata: object.id is a string,
// participant_uuid is per join (and per device), participant_left has NO join_time.

import crypto from 'node:crypto'

import { serverUrl, WEBHOOK_SECRET } from './env'

export type ZoomEventType =
  | 'meeting.started'
  | 'meeting.ended'
  | 'meeting.participant_joined'
  | 'meeting.participant_left'

export interface Meeting {
  /** Meeting ID, digits only (11 digits, random per test so tests never share a meeting). */
  id: string
  /** Meeting UUID of this Zoom session. */
  uuid: string
  hostId: string
  accountId: string
  topic: string
  start: Date
}

export interface Participant {
  name: string
  /** Per join / per device. */
  uuid: string
  userId: string
}

/** One webhook: what sendWebhook needs. */
export interface ZoomEvent {
  type: ZoomEventType
  payload: Record<string, unknown>
  /** event_ts (ms). */
  eventTs: number
}

const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'

function token(n: number): string {
  let s = ''
  for (let i = 0; i < n; i++) s += ALPHABET[crypto.randomInt(ALPHABET.length)]
  return s
}

/** Random 11-digit meeting ID ("8xxxxxxxxxx"). */
export function randomMeetingId(): string {
  let s = String(8 + crypto.randomInt(2))
  for (let i = 0; i < 10; i++) s += String(crypto.randomInt(10))
  return s
}

/** "857 4606 5432" — Zoom's grouping of an 11-digit ID. */
export function formatMeetingId(id: string): string {
  if (id.length === 11) return `${id.slice(0, 3)} ${id.slice(3, 7)} ${id.slice(7)}`
  if (id.length === 10) return `${id.slice(0, 3)} ${id.slice(3, 6)} ${id.slice(6)}`
  return id
}

export function newMeeting(start: Date, id = randomMeetingId(), topic = 'E2E занятие'): Meeting {
  return {
    id,
    uuid: crypto.randomBytes(16).toString('base64'),
    hostId: token(22),
    accountId: token(22),
    topic,
    start,
  }
}

let nextUserId = 16778240

/** A new join (device): its own participant_uuid and user_id, as Zoom does. */
export function participant(name: string): Participant {
  nextUserId += 1024 + crypto.randomInt(1000)
  return { name, uuid: crypto.randomUUID().toUpperCase(), userId: String(nextUserId) }
}

/** Zoom writes times with second precision. */
export function zoomTime(d: Date): string {
  return new Date(Math.floor(d.getTime() / 1000) * 1000).toISOString().replace('.000Z', 'Z')
}

export function minutes(n: number): number {
  return n * 60_000
}

export function addMinutes(d: Date, n: number): Date {
  return new Date(d.getTime() + minutes(n))
}

function object(m: Meeting): Record<string, unknown> {
  return {
    id: m.id,
    uuid: m.uuid,
    host_id: m.hostId,
    topic: m.topic,
    type: 8,
    start_time: zoomTime(m.start),
    timezone: 'Europe/Moscow',
    duration: 90,
  }
}

function envelopePayload(m: Meeting, obj: Record<string, unknown>): Record<string, unknown> {
  return { account_id: m.accountId, object: obj }
}

export function meetingStarted(m: Meeting): ZoomEvent {
  return {
    type: 'meeting.started',
    eventTs: m.start.getTime(),
    payload: envelopePayload(m, { ...object(m), host_email: 'teacher@school.example' }),
  }
}

export function meetingEnded(m: Meeting, at: Date): ZoomEvent {
  return {
    type: 'meeting.ended',
    eventTs: at.getTime(),
    payload: envelopePayload(m, { ...object(m), host_email: 'teacher@school.example', end_time: zoomTime(at) }),
  }
}

export function participantJoined(m: Meeting, p: Participant, at: Date): ZoomEvent {
  return {
    type: 'meeting.participant_joined',
    eventTs: at.getTime(),
    payload: envelopePayload(m, {
      ...object(m),
      participant: {
        user_id: p.userId,
        user_name: p.name,
        id: '',
        participant_uuid: p.uuid,
        join_time: zoomTime(at),
        email: '',
        registrant_id: '',
        participant_user_id: '',
        customer_key: '',
        phone_number: '',
        public_ip: '203.0.113.17',
      },
    }),
  }
}

/** participant_left: like real Zoom payloads, without join_time. */
export function participantLeft(
  m: Meeting,
  p: Participant,
  at: Date,
  reason = 'left the meeting. Reason : left the meeting',
): ZoomEvent {
  return {
    type: 'meeting.participant_left',
    eventTs: at.getTime(),
    payload: envelopePayload(m, {
      ...object(m),
      participant: {
        leave_time: zoomTime(at),
        public_ip: '203.0.113.17',
        user_id: p.userId,
        user_name: p.name,
        registrant_id: '',
        participant_user_id: '',
        id: '',
        leave_reason: reason,
        email: '',
        private_ip: '192.168.1.23',
        participant_uuid: p.uuid,
      },
    }),
  }
}

/** Raw JSON body of a webhook (the bytes that get signed). */
export function webhookBody(type: string, payload: Record<string, unknown>, eventTs = Date.now()): string {
  return JSON.stringify({ event: type, event_ts: eventTs, payload })
}

/** Zoom's signature headers for `body` sent now. */
export function signHeaders(body: string, secret = WEBHOOK_SECRET, now = Date.now()): Record<string, string> {
  const ts = String(Math.floor(now / 1000))
  const sig = crypto.createHmac('sha256', secret).update(`v0:${ts}:${body}`).digest('hex')
  return {
    'content-type': 'application/json; charset=utf-8',
    'user-agent': 'Zoom Marketplace/1.0a',
    'x-zm-request-timestamp': ts,
    'x-zm-signature': `v0=${sig}`,
  }
}

export interface SendOptions {
  baseUrl?: string
  secret?: string
}

/** POSTs raw bytes, signed; throws unless the server answers 2xx. Returns the response text. */
export async function postWebhookBody(body: string, opts: SendOptions = {}): Promise<string> {
  const base = opts.baseUrl ?? serverUrl()
  const res = await fetch(`${base}/api/webhooks/zoom`, {
    method: 'POST',
    headers: signHeaders(body, opts.secret),
    body,
  })
  const text = await res.text()
  if (!res.ok) throw new Error(`webhook ${res.status}: ${text.slice(0, 300)}`)
  return text
}

/** Sends one Zoom webhook (`type` + `payload`), signed like Zoom. Returns the raw body sent. */
export async function sendWebhook(
  type: string,
  payload: Record<string, unknown>,
  opts: SendOptions & { eventTs?: number } = {},
): Promise<string> {
  const body = webhookBody(type, payload, opts.eventTs ?? Date.now())
  await postWebhookBody(body, opts)
  return body
}

/** Sends a built event (with its own event_ts). */
export async function sendEvent(ev: ZoomEvent, opts: SendOptions = {}): Promise<string> {
  return sendWebhook(ev.type, ev.payload, { ...opts, eventTs: ev.eventTs })
}

/** Sends events one after another (order matters for the server's event ids). */
export async function sendEvents(events: ZoomEvent[], opts: SendOptions = {}): Promise<void> {
  for (const ev of events) await sendEvent(ev, opts)
}

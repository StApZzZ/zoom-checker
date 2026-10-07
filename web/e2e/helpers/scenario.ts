// Common setup of a spec: a fresh group (pasted list), a lesson with its own
// Zoom meeting, the live screen open and (optionally) the meeting started.

import { expect, type Page } from '@playwright/test'

import { meetingStarted, newMeeting, sendEvent, type Meeting } from './zoom'
import {
  createGroupByPaste,
  createLesson,
  lessonStatusBadge,
  minutesAgo,
  openLesson,
  uniq,
  type GroupRef,
  type LessonRef,
} from './ui'

export interface LiveLesson {
  group: GroupRef
  lesson: LessonRef
  meeting: Meeting
  /** Planned start of the lesson = start of the Zoom meeting (a whole minute). */
  start: Date
}

export interface LiveLessonOptions {
  /** Lesson (and meeting) started this many minutes ago. Default 10. */
  startedMinutesAgo?: number
  /** Planned duration. Default 90. */
  durationMinutes?: number
  /** Send meeting.started (lesson becomes "Идёт"). Default true. */
  startMeeting?: boolean
  /** Zoom field input; default a plain join link of the meeting. */
  zoomInput?: (meeting: Meeting) => string
  /** Server that receives the webhooks (default: the shared one). */
  baseUrl?: string
}

export async function liveLesson(
  page: Page,
  prefix: string,
  students: string[],
  opts: LiveLessonOptions = {},
): Promise<LiveLesson> {
  const group = await createGroupByPaste(page, uniq(prefix), students)
  const start = minutesAgo(opts.startedMinutesAgo ?? 10)
  const meeting = newMeeting(start)
  const zoomInput = opts.zoomInput?.(meeting) ?? `https://us02web.zoom.us/j/${meeting.id}?pwd=e2eTest.1`
  const lesson = await createLesson(page, group, zoomInput, start, { durationMinutes: opts.durationMinutes ?? 90 })
  await openLesson(page, lesson)
  if (opts.startMeeting !== false) {
    await sendEvent(meetingStarted(meeting), { baseUrl: opts.baseUrl })
    await expect(lessonStatusBadge(page, 'Идёт')).toBeVisible()
  }
  return { group, lesson, meeting, start }
}

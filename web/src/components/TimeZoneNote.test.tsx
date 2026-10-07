import { screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { TimeZoneNote } from '@/components/TimeZoneNote'
import { browserTimeZone, utcOffsetLabel } from '@/lib/format'
import type { AuthConfig } from '@/lib/types'
import { mockFetch, renderWithClient } from '@/test/utils'

const config = (timezone?: string): AuthConfig => ({
  local_enabled: true,
  ldap_enabled: false,
  oidc_enabled: false,
  oidc_label: '',
  version: '1',
  ...(timezone === undefined ? {} : { timezone }),
})

// UTC+14: no test machine runs there, so the zones always differ.
const FAR_ZONE = 'Pacific/Kiritimati'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('TimeZoneNote', () => {
  it('says which zone the date and time fields use when the server has another one', async () => {
    mockFetch({ 'GET /api/auth/config': config(FAR_ZONE) })
    renderWithClient(<TimeZoneNote at="2026-10-06T15:00:00Z" />)
    const mine = utcOffsetLabel(browserTimeZone(), '2026-10-06T15:00:00Z')
    expect(
      await screen.findByText(`Время указывается по вашему часовому поясу (${mine}), а не по времени сервиса (UTC+14).`),
    ).toBeInTheDocument()
  })

  it('explains differences in files and messages on the page variant', async () => {
    mockFetch({ 'GET /api/auth/config': config(FAR_ZONE) })
    renderWithClient(<TimeZoneNote variant="page" />)
    expect(await screen.findByText(/Сервис работает по времени UTC\+14: в его сообщениях время указано с поясом/)).toBeInTheDocument()
  })

  it.each([
    ['the same zone', browserTimeZone()],
    ['no zone from an older server', undefined],
    ['a zone the browser does not know', 'Mars/Olympus'],
  ])('shows nothing for %s', async (_name, timezone) => {
    const fetchMock = mockFetch({ 'GET /api/auth/config': config(timezone) })
    const { container } = renderWithClient(<TimeZoneNote />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 20))
    expect(container).toBeEmptyDOMElement()
  })
})

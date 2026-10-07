import { render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import App from '@/App'
import { errorResponse, mockFetch } from '@/test/utils'

afterEach(() => {
  vi.unstubAllGlobals()
})

it('boots with all providers and shows the login page to anonymous users', async () => {
  const consoleError = vi.spyOn(console, 'error')
  mockFetch({
    'GET /api/auth/config': { local_enabled: true, ldap_enabled: true, oidc_enabled: true, oidc_label: 'Войти через Keycloak', version: 'dev' },
    'GET /api/auth/me': () => errorResponse(401, 'unauthorized', 'Нужно войти'),
  })
  render(<App />)
  expect(await screen.findByLabelText('Логин')).toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'Войти через Keycloak' })).toBeInTheDocument()
  expect(window.location.pathname).toBe('/login')
  expect(consoleError).not.toHaveBeenCalled()
})

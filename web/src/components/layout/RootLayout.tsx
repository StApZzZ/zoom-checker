import { Outlet } from 'react-router'

import { AuthProvider } from '@/lib/auth'

/** Root route element: session context for every page (login included). */
export function RootLayout() {
  return (
    <AuthProvider>
      <Outlet />
    </AuthProvider>
  )
}

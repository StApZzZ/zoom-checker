// Route table (React Router library mode). Kept separate from App.tsx so tests
// can mount it with createMemoryRouter.

import type { ComponentType } from 'react'
import { Navigate, type RouteObject } from 'react-router'

import { AppShell } from '@/components/layout/AppShell'
import { RootLayout } from '@/components/layout/RootLayout'
import { PageSpinner } from '@/components/LoadingState'
import { RequireAdmin, RequireAuth } from '@/lib/auth'
import LoginPage from '@/pages/auth/LoginPage'
import NotFoundPage from '@/pages/errors/NotFoundPage'
import RouteErrorPage from '@/pages/errors/RouteErrorPage'

/** Lazy route from a module whose default export is the page component. */
const page = (load: () => Promise<{ default: ComponentType }>) => async () => ({
  Component: (await load()).default,
})

export const routes: RouteObject[] = [
  {
    element: <RootLayout />,
    errorElement: <RouteErrorPage fullScreen />,
    hydrateFallbackElement: <PageSpinner className="min-h-svh" />,
    children: [
      { path: '/login', element: <LoginPage /> },
      {
        element: (
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        ),
        children: [
          {
            // Errors of a page render inside the shell, keeping the navigation.
            errorElement: <RouteErrorPage />,
            children: [
              { index: true, lazy: page(() => import('@/pages/lessons/LessonsPage')) },
              { path: 'lessons/:id', lazy: page(() => import('@/pages/lessons/LessonPage')) },
              { path: 'groups', lazy: page(() => import('@/pages/groups/GroupsPage')) },
              { path: 'groups/:id', lazy: page(() => import('@/pages/groups/GroupPage')) },
              { path: 'groups/:id/report', lazy: page(() => import('@/pages/groups/GroupReportPage')) },
              { path: 'zoom', lazy: page(() => import('@/pages/zoom/ZoomPage')) },
              { path: 'help', lazy: page(() => import('@/pages/help/HelpPage')) },
              {
                path: 'admin',
                element: <RequireAdmin />,
                children: [
                  { index: true, element: <Navigate to="/admin/users" replace /> },
                  { path: 'users', lazy: page(() => import('@/pages/admin/UsersPage')) },
                  { path: 'settings', lazy: page(() => import('@/pages/admin/SettingsPage')) },
                  { path: 'events', lazy: page(() => import('@/pages/admin/EventsPage')) },
                ],
              },
              { path: '*', element: <NotFoundPage /> },
            ],
          },
        ],
      },
    ],
  },
]

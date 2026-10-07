import { QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider } from 'next-themes'
import { createBrowserRouter } from 'react-router'
import { RouterProvider } from 'react-router/dom'

import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { createQueryClient } from '@/lib/queries'
import { routes } from '@/routes'

const queryClient = createQueryClient()
const router = createBrowserRouter(routes)

/** Global providers: theme, server state, tooltips, toasts, router. */
export default function App() {
  return (
    // The anti-flash script is public/theme-init.js (CSP forbids inline scripts); next-themes'
    // own inline script is marked as a data block so React 19 does not warn about it.
    <ThemeProvider
      attribute="class"
      defaultTheme="system"
      enableSystem
      disableTransitionOnChange
      scriptProps={{ type: 'application/json' }}
    >
      <QueryClientProvider client={queryClient}>
        <TooltipProvider delayDuration={300}>
          <RouterProvider router={router} />
          <Toaster richColors position="top-center" closeButton />
        </TooltipProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
}

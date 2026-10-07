import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach, vi } from 'vitest'

import { resetReceipts } from '@/lib/liveClock'

afterEach(() => {
  cleanup()
  resetReceipts()
  localStorage.clear()
})

// jsdom lacks these browser APIs used by Radix UI and next-themes.
if (!window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  })
}

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (!('ResizeObserver' in window)) {
  Object.defineProperty(window, 'ResizeObserver', { writable: true, value: ResizeObserverStub })
}

const proto = window.HTMLElement.prototype as unknown as Record<string, unknown>
proto.hasPointerCapture ??= () => false
proto.setPointerCapture ??= () => {}
proto.releasePointerCapture ??= () => {}
proto.scrollIntoView ??= () => {}

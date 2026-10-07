// Minimal EventSource double for SSE tests.

type Listener = (ev: MessageEvent<string>) => void

export class FakeEventSource {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  static instances: FakeEventSource[] = []

  readonly CONNECTING = 0
  readonly OPEN = 1
  readonly CLOSED = 2

  url: string
  readyState = FakeEventSource.CONNECTING
  onopen: ((ev: Event) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  closed = false
  private listeners = new Map<string, Set<Listener>>()

  constructor(url: string | URL) {
    this.url = String(url)
    FakeEventSource.instances.push(this)
  }

  addEventListener(type: string, listener: Listener) {
    let set = this.listeners.get(type)
    if (!set) {
      set = new Set()
      this.listeners.set(type, set)
    }
    set.add(listener)
  }

  removeEventListener(type: string, listener: Listener) {
    this.listeners.get(type)?.delete(listener)
  }

  close() {
    this.closed = true
    this.readyState = FakeEventSource.CLOSED
  }

  // --- test helpers ---

  open() {
    this.readyState = FakeEventSource.OPEN
    this.onopen?.(new Event('open'))
  }

  emit(type: string, data: string) {
    const ev = new MessageEvent(type, { data })
    for (const l of this.listeners.get(type) ?? []) l(ev)
  }

  /** Transient error: the browser keeps retrying (readyState CONNECTING). */
  drop() {
    this.readyState = FakeEventSource.CONNECTING
    this.onerror?.(new Event('error'))
  }

  /** Fatal error: the browser gave up (readyState CLOSED). */
  fail() {
    this.readyState = FakeEventSource.CLOSED
    this.onerror?.(new Event('error'))
  }

  static latest(): FakeEventSource {
    const last = FakeEventSource.instances.at(-1)
    if (!last) throw new Error('no EventSource created')
    return last
  }

  static reset() {
    FakeEventSource.instances = []
  }
}

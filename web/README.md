# Zoom Checker — web (SPA)

React 19 + TypeScript + Vite + Tailwind CSS 4 + shadcn/ui + TanStack Query 5 + React Router (library mode).
The build output (`dist/`) is embedded into the Go binary by `web/embed.go`.

```bash
npm run dev      # http://localhost:5173, proxies /api, /auth, /healthz to :8080
npm run build    # tsc -b (zero errors) + vite build → dist/
npm run lint     # oxlint
npm test         # vitest (jsdom)
```

## Where things live

| Path | What |
|---|---|
| `src/lib/types.ts` | API shapes, mirror `docs/architecture.md` §6 (+ marked client expectations for shapes §6 leaves open) |
| `src/lib/api.ts` | the only place that calls `fetch`: `api.auth / groups / students / lessons / zoom / admin`, `ApiError`, `errorMessage`, `downloadFile` |
| `src/lib/queries.ts` | `queryKeys`, read hooks (`useGroups`, `useLessonView`, …), mutation hooks that invalidate the right keys |
| `src/lib/sse.ts` | `useLiveLessonView(id)` / `useLessonStream(id)` (live screen), `useGlobalStream()` (lesson lists) |
| `src/lib/format.ts` | Russian dates, durations, `plural` / `pluralize` + `WORDS` |
| `src/lib/auth.tsx` | `useAuth()` (`me`, `isAdmin`, `logout`, `openChangePassword`), `RequireAuth`, `RequireAdmin` |
| `src/hooks/` | `useNow`, `useLiveSeconds`, `useDocumentTitle` |
| `src/components/` | shared blocks: `StatusBadge`, `FinalStatusBadge`, `LiveDuration`, `StatTile`, `EmptyState`, `ErrorState`, `LoadingState`, `ConfirmDialog`, `CopyButton`; labels in `status-meta.ts`, colors in `tone.ts` |
| `src/components/layout/` | `AppShell` (top bar + nav), `Page` (container), `PageHeader` (title/description/actions, sets the tab title) |
| `src/components/ui/` | shadcn/ui primitives (`npx shadcn@latest add <name> -y`) |
| `src/pages/<area>/<Page>.tsx` | one default-exported component per route, lazy loaded from `src/routes.tsx` |

## Page recipe

```tsx
export default function GroupsPage() {
  const groups = useGroups('active')
  return (
    <Page>
      <PageHeader title="Группы" description="…" actions={<Button>Новая группа</Button>} />
      {groups.data ? (
        groups.data.length === 0 ? (
          <EmptyState icon={Users} title="Групп пока нет" text="Создайте группу и вставьте список студентов." />
        ) : (
          /* … */
        )
      ) : groups.isError ? (
        <ErrorState error={groups.error} onRetry={() => groups.refetch()} retrying={groups.isFetching} />
      ) : (
        <LoadingState variant="cards" />
      )}
    </Page>
  )
}
```

Rules of thumb:

- Check `data` before `isError`: when a background refetch fails (polling while the live stream is
  down, window focus, invalidation) TanStack Query keeps the previous `data` **and** sets `isError`.
  Show the full-page `ErrorState` only when there is no data; otherwise keep the data on screen
  (optionally with a small warning).
- Server state only through `@/lib/queries` hooks (add new hooks/keys there, never call `fetch`).
- Mutations toast errors globally; pass `{ errorToast: false }` when the page shows the error inline,
  and `{ successMessage }` to override the default success toast.
- File downloads (exports) go through `useLessonExport` / `useGroupReportExport` (fetch → Blob → save):
  a 401 goes to the login page, other errors toast the server's message, `pending` drives the
  buttons' spinner. Never link an API URL with a bare `<a download>` (an error would be saved as a file).
- Lesson actions answer with a `LessonView`; the mutation hooks store it when it is newer than the
  cached one (`storeLessonView`) instead of refetching. Был/Не был and comment saves are not written
  into the cache: `useLessonView` overlays pending `useUpdateAttendance` inputs on the server view, so
  live snapshots cannot flip a toggle back while it is being saved.
- Live lesson screen: `const { data, connection } = useLiveLessonView(id)`; durations of online rows via
  `<LiveDuration seconds={row.total_seconds} asOf={data.as_of} online={row.current_status === 'online'} />`;
  connection state via `<StatusBadge kind="stream" status={connection} />`. Live counters are
  skew-proof: they count from the snapshot's client receive time, plus the snapshot's age on arrival
  measured on the server clock (`as_of` vs. the HTTP `Date` header) — see `src/lib/liveClock.ts`.
- Labels: `status-meta.ts` has the Russian wording for every status, plus `auditActionLabel(action)`
  for audit entries and `importRowStatusMeta` / `<StatusBadge kind="import">` for the import preview.
- Lookups by a value that comes from the URL or the server use own keys only
  (`Object.hasOwn(table, key)`): `table['__proto__']` must not reach `Object.prototype`.
- Lesson lists with live counts: call `useGlobalStream()` on that page only (each stream holds a connection).
- Status is never color-only: use `StatusBadge` / `FinalStatusBadge` (icon + text + color).
- UI copy is Russian, short and friendly; no jargon.

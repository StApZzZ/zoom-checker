/** True for "failed to fetch dynamically imported module" errors (stale chunk after a redeploy). */
export function isChunkLoadError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return (
    /dynamically imported module|Importing a module script failed|error loading dynamically imported module/i.test(
      error.message,
    ) || error.name === 'ChunkLoadError'
  )
}

/** aria-describedby value for a control inside <FormField> (hint and/or error ids). */
export function describedBy(id: string, hint?: unknown, error?: unknown): string | undefined {
  const ids = [hint ? `${id}-hint` : '', error ? `${id}-error` : ''].filter(Boolean)
  return ids.length ? ids.join(' ') : undefined
}

/** A JSON number literal with 16+ digits: JSON.parse would silently round it. */
const UNSAFE_NUMBER = /[:[,]\s*-?\d{16,}/

/**
 * Pretty-prints a raw webhook body for reading (2-space indent). The stored
 * body is byte-for-byte what Zoom sent; when it is not valid JSON, or contains
 * numbers too long to survive JSON.parse, it is shown as is (`valid: false`).
 */
export function prettyJson(raw: string | null | undefined): { text: string; valid: boolean } {
  const text = raw ?? ''
  if (!text.trim()) return { text: '', valid: false }
  if (UNSAFE_NUMBER.test(text)) return { text, valid: false }
  try {
    return { text: JSON.stringify(JSON.parse(text), null, 2), valid: true }
  } catch {
    return { text, valid: false }
  }
}

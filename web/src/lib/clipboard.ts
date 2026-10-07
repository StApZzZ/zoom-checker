/**
 * Copies text to the clipboard. Uses the async Clipboard API when available and
 * falls back to execCommand('copy'), which also works on plain-HTTP intranet
 * installs where navigator.clipboard is undefined (not a secure context).
 */
export async function copyToClipboard(text: string): Promise<boolean> {
  if (typeof navigator !== 'undefined' && navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Permission denied or document not focused: try the legacy path.
    }
  }
  return legacyCopy(text)
}

function legacyCopy(text: string): boolean {
  const el = document.createElement('textarea')
  el.value = text
  el.setAttribute('readonly', '')
  el.style.position = 'fixed'
  el.style.top = '0'
  el.style.left = '0'
  el.style.opacity = '0'
  document.body.appendChild(el)
  const selection = document.getSelection()
  const previous = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null
  el.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  el.remove()
  if (previous && selection) {
    selection.removeAllRanges()
    selection.addRange(previous)
  }
  return ok
}

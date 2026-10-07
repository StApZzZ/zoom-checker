// Sets the "dark" class before React starts so there is no light flash.
// Mirrors next-themes (attribute="class", storageKey "theme", system default).
;(function () {
  try {
    var stored = localStorage.getItem('theme')
    var theme = stored === 'light' || stored === 'dark' ? stored : 'system'
    var dark = theme === 'dark' || (theme === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
    var root = document.documentElement
    root.classList.add(dark ? 'dark' : 'light')
    root.style.colorScheme = dark ? 'dark' : 'light'
  } catch {
    /* storage blocked: keep the default light theme */
  }
})()

'use strict'
// Renderer side of the splash window: pure DOM updates driven by state messages from the main process.
const $ = (id) => document.getElementById(id)
let indetTimer = null
function arm() {
  clearTimeout(indetTimer)
  indetTimer = setTimeout(() => $('bar').classList.add('indet'), 9000) // progress not advancing: show an indeterminate bar
}
window.oaSplash.onState((s) => {
  if (s.version) $('ver').textContent = '版本 ' + s.version
  if (s.error) {
    document.body.classList.add('err')
    $('etitle').textContent = s.error.title
    $('ehint').textContent = s.error.hint
    clearTimeout(indetTimer)
    return
  }
  document.body.classList.remove('err')
  if (typeof s.pct === 'number') {
    $('bar').classList.remove('indet')
    $('fill').style.width = s.pct + '%'
    arm()
  }
  if (s.text) $('status').textContent = s.text
  $('hint').textContent = s.hint || ''
})
document.querySelectorAll('button[data-a]').forEach((b) => b.addEventListener('click', () => window.oaSplash.action(b.dataset.a)))
arm()

// Finds content that is cut off: (1) overlapped by the sticky page header at scroll 0, (2) clipped by an overflow:hidden/clip
// ancestor, (3) wider than the window. Mock mode: UI_BASE=http://127.0.0.1:5173 node e2e/ui_clip.mjs  (UI_DPR / UI_SIZES optional)
import puppeteer from 'puppeteer-core'
const base = process.env.UI_BASE ?? 'http://127.0.0.1:5173'
const exe = process.env.UI_BROWSER ?? 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
const dpr = Number(process.env.UI_DPR ?? 1)
const sizes = (process.env.UI_SIZES ?? '2000x1000,1440x900,1024x768,800x900').split(',').map((s) => s.split('x').map(Number))
const pages = ['/', '/import', '/ai', '/tasks?mock_tasks=all', '/files', '/manage', '/storage', '/ledger', '/templates', '/settings', '/admin/users', '/admin/security', '/admin/sessions', '/admin/audit', '/admin/diagnostics', '/account/security']
const browser = await puppeteer.launch({ executablePath: exe, headless: 'new', args: ['--no-sandbox'] })
let bad = 0
for (const [w, h] of sizes) {
  const page = await browser.newPage()
  await page.setViewport({ width: w, height: h, deviceScaleFactor: dpr })
  for (const url of pages) {
    await page.goto(base + url, { waitUntil: 'networkidle0' })
    await new Promise((r) => setTimeout(r, 400))
    const found = await page.evaluate(() => {
      const out = []
      const main = document.querySelector('main')
      if (!main) return out
      const ph = document.querySelector('.ph')
      const phb = ph ? ph.getBoundingClientRect().bottom : null
      const vis = (el) => { const r = el.getBoundingClientRect(); const cs = getComputedStyle(el); return r.width > 2 && r.height > 2 && cs.visibility !== 'hidden' && cs.display !== 'none' }
      const desc = (el) => el.tagName.toLowerCase() + (el.id ? '#' + el.id : '') + (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\s+/).slice(0, 3).join('.') : '') + ' "' + (el.textContent || '').trim().slice(0, 24) + '"'
      for (const el of main.querySelectorAll('*')) {
        if (!vis(el) || el.closest('.ph') || el.classList.contains('page')) continue
        const r = el.getBoundingClientRect()
        if (phb !== null && r.top < phb - 1 && r.bottom > phb + 1 && main.scrollTop === 0) out.push('under header: ' + desc(el))
        for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
          const cs = getComputedStyle(p)
          if (/(auto|scroll)/.test(cs.overflowX + cs.overflowY) && p !== main) break // reachable by scrolling: not cut off
          if (/(hidden|clip)/.test(cs.overflowX + cs.overflowY) && p !== main) {
            const pr = p.getBoundingClientRect()
            const bleedX = Math.max(pr.left - r.left, r.right - pr.right)
            const bleedY = Math.max(pr.top - r.top, r.bottom - pr.bottom)
            if ((bleedX > 3 || bleedY > 3) && r.width < pr.width * 3) { out.push(`clipped by ${desc(p)} (${Math.round(bleedX)}x${Math.round(bleedY)}px): ${desc(el)}`); break }
          }
        }
      }
      if (document.documentElement.scrollWidth > document.documentElement.clientWidth + 1) out.push('page wider than window by ' + (document.documentElement.scrollWidth - document.documentElement.clientWidth) + 'px')
      return [...new Set(out)].slice(0, 6)
    })
    for (const f of found) { bad++; console.log(`${w}x${h} ${url}  ${f}`) }
  }
  await page.close()
}
await browser.close()
console.log(bad ? `${bad} possible cut-off(s)` : 'no cut-off found')

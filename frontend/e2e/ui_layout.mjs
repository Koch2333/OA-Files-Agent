// Layout regression check (mock mode): the sticky PageHeader must not overlap the first block under it at scroll 0.
//   UI_BASE=http://127.0.0.1:5173 node e2e/ui_layout.mjs
import puppeteer from 'puppeteer-core'
const base = process.env.UI_BASE ?? 'http://127.0.0.1:5199'
const exe = process.env.UI_BROWSER ?? 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
const browser = await puppeteer.launch({ executablePath: exe, headless: 'new', args: ['--no-sandbox'] })
let failed = 0
const page = await browser.newPage()
await page.setViewport({ width: 1440, height: 900 })
for (const url of ['/', '/files', '/tasks', '/ledger']) {
  await page.goto(base + url, { waitUntil: 'networkidle0' })
  await new Promise((r) => setTimeout(r, 500))
  const r = await page.evaluate(() => {
    const ph = document.querySelector('.ph')
    const main = document.querySelector('main')
    if (!ph) return { none: true }
    const cs = getComputedStyle(ph)
    const pr = ph.getBoundingClientRect()
    const sib = ph.nextElementSibling
    const sr = sib ? sib.getBoundingClientRect() : null
    return { scrollTop: main.scrollTop, headerTop: Math.round(pr.top), headerBottom: Math.round(pr.bottom), marginBottom: cs.marginBottom, marginTop: cs.marginTop, position: cs.position, sibTag: sib && (sib.tagName + '.' + sib.className), sibTop: sr && Math.round(sr.top), gap: sr && Math.round(sr.top - pr.bottom), mainPadTop: getComputedStyle(main).paddingTop, mainTop: Math.round(main.getBoundingClientRect().top) }
  })
  console.log(url, JSON.stringify(r))
  if (!r.none && (r.gap < 0 || r.scrollTop !== 0)) { console.log("FAIL  header overlaps the next block on " + url + " (gap " + r.gap + "px)"); failed++ }
}
await browser.close()
console.log(failed ? failed + " layout check(s) FAILED" : "layout ok: the sticky page header never overlaps the content below it")
process.exit(failed ? 1 : 0)

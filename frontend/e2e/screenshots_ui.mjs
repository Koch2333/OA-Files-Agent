// Visual pass for the workspace layout (mock/demo data): node e2e/screenshots_ui.mjs
// Env: UI_BASE (default http://127.0.0.1:5173, a Vite dev server in mock mode), UI_BROWSER, UI_OUT (default e2e/screenshots/ui).
// Writes <page>-<width>-<light|dark>.png and reports horizontal overflow per page/width (the 800 px guarantee).
import puppeteer from 'puppeteer-core'
import fs from 'node:fs'
import path from 'node:path'

const base = process.env.UI_BASE ?? 'http://127.0.0.1:5173'
const exe = process.env.UI_BROWSER ?? 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe'
const out = path.resolve(process.env.UI_OUT ?? 'e2e/screenshots/ui')
fs.mkdirSync(out, { recursive: true })
const pages = [['home', '/'], ['tasks', '/tasks?mock_tasks=all'], ['ledger', '/ledger'], ['import', '/import'], ['ai', '/ai']]
const sizes = [[1440, 900], [1024, 768], [800, 900], [390, 844]]
const browser = await puppeteer.launch({ executablePath: exe, headless: 'new', args: ['--no-sandbox'] })
let overflow = 0
for (const theme of ['light', 'dark']) {
  for (const [w, h] of sizes) {
    const page = await browser.newPage()
    await page.setViewport({ width: w, height: h })
    await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: theme }])
    for (const [name, url] of pages) {
      await page.goto(base + url, { waitUntil: 'networkidle0' })
      await new Promise((r) => setTimeout(r, 600))
      const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      if (wide > 1) { overflow++; console.log(`OVERFLOW ${name} ${w}px (${theme}) by ${wide}px`) }
      await page.screenshot({ path: path.join(out, `${name}-${w}-${theme}.png`), fullPage: false })
    }
    await page.close()
  }
}
await browser.close()
console.log(overflow ? `${overflow} overflow case(s)` : 'no horizontal overflow')

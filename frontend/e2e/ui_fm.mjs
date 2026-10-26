// File manager multi-select + conflict dialog in demo (mock) mode.
//   npx vite --port 5199   (mock data is the dev default)   then   UI_BASE=http://127.0.0.1:5199 node e2e/ui_fm.mjs
import { createRunner, launch, sleep } from './lib/common.mjs'
const base = process.env.UI_BASE ?? 'http://127.0.0.1:5199'
const t = createRunner('ui_fm')
const { browser, page } = await launch()
const errors = []
page.on('pageerror', (e) => errors.push(String(e)))
await page.goto(base + '/manage?path=' + encodeURIComponent('归档'), { waitUntil: 'networkidle0' })
await page.waitForSelector('[data-testid=fm-row]', { timeout: 15000 })
const rowCount = await page.$$eval('[data-testid=fm-row]', (r) => r.length)
t.check('rows have checkboxes', (await page.$$('[data-testid=fm-select]')).length === rowCount && rowCount >= 2, rowCount)
const boxes = await page.$$('[data-testid=fm-select]')
await boxes[0].click()
t.check('selection bar appears with a count', (await page.$eval('[data-testid=fm-selcount]', (e) => e.textContent)).includes('已选 1 项'))
await page.keyboard.down('Shift'); await boxes[Math.min(2, boxes.length - 1)].click(); await page.keyboard.up('Shift')
const n = await page.$eval('[data-testid=fm-selcount]', (e) => e.textContent)
t.check('shift-click selects a range', /已选 (2|3) 项/.test(n), n)
await page.click('[data-testid=fm-select-all]')
t.check('select-all selects every visible row', (await page.$eval('[data-testid=fm-selcount]', (e) => e.textContent)).includes(`已选 ${rowCount} 项`))
await page.click('[data-testid=fm-select-all]')
t.check('select-all again clears', !(await page.$('[data-testid=fm-selbar]')))
await page.focus('[data-testid=fm-row]'); await page.keyboard.down('Control'); await page.keyboard.press('a'); await page.keyboard.up('Control')
t.check('ctrl+A selects all', (await page.$eval('[data-testid=fm-selcount]', (e) => e.textContent)).includes(`已选 ${rowCount} 项`))
await page.screenshot({ path: `${t.shots}/fm-select.png` })
await page.keyboard.press('Escape')
t.check('Escape clears the selection', !(await page.$('[data-testid=fm-selbar]')))
// navigating clears the selection
await boxes[0].click()
await page.goto(base + '/manage', { waitUntil: 'networkidle0' })
t.check('navigation clears the selection', !(await page.$('[data-testid=fm-selbar]')))
// hash chip on a task-related file
await page.goto(base + '/manage?path=' + encodeURIComponent('归档'), { waitUntil: 'networkidle0' })
await sleep(300)
const chips = await page.$$('[data-testid=fm-hash]')
t.check('task-related files show a hash status chip', chips.length >= 0)
await browser.close()
t.finish(errors)

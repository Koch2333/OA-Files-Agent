'use strict'
// Renders build/icon.svg to build/icon.png (512) and build/icon.ico (16..256) using headless Edge/Chrome (no npm deps).
// Run once after editing the SVG:  npm run icons   (outputs are committed).
const { execFileSync } = require('child_process')
const fs = require('fs')
const os = require('os')
const path = require('path')

const root = path.join(__dirname, '..', 'build')
const svg = fs.readFileSync(path.join(root, 'icon.svg'), 'utf8')
const candidates = [process.env.OA_BROWSER, 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe', 'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe', 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe'].filter(Boolean)
const browser = candidates.find((p) => fs.existsSync(p))
if (!browser) throw new Error('Edge/Chrome not found (set OA_BROWSER)')
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'oa-icons-'))

function render(size, out) {
  const html = path.join(tmp, `i${size}.html`)
  fs.writeFileSync(html, `<!doctype html><html><body style="margin:0;background:transparent"><div style="width:${size}px;height:${size}px">${svg.replace(/width="256" height="256"/, `width="${size}" height="${size}"`)}</div></body></html>`)
  execFileSync(browser, ['--headless=new', '--disable-gpu', '--hide-scrollbars', '--default-background-color=00000000', `--window-size=${size},${size}`, `--screenshot=${out}`, '--user-data-dir=' + path.join(tmp, 'profile'), 'file:///' + html.replace(/\\/g, '/')], { stdio: 'ignore' })
}

const sizes = [16, 24, 32, 48, 64, 128, 256]
const pngs = sizes.map((s) => {
  const f = path.join(tmp, `${s}.png`)
  render(s, f)
  return { s, buf: fs.readFileSync(f) }
})
render(512, path.join(root, 'icon.png'))
// ICO container with embedded PNG images
const head = Buffer.alloc(6)
head.writeUInt16LE(1, 2)
head.writeUInt16LE(pngs.length, 4)
let offset = 6 + 16 * pngs.length
const dir = pngs.map(({ s, buf }) => {
  const e = Buffer.alloc(16)
  e[0] = s === 256 ? 0 : s
  e[1] = s === 256 ? 0 : s
  e.writeUInt16LE(1, 4)
  e.writeUInt16LE(32, 6)
  e.writeUInt32LE(buf.length, 8)
  e.writeUInt32LE(offset, 12)
  offset += buf.length
  return e
})
fs.writeFileSync(path.join(root, 'icon.ico'), Buffer.concat([head, ...dir, ...pngs.map((p) => p.buf)]))
fs.rmSync(tmp, { recursive: true, force: true })
console.log('icons written to', root)

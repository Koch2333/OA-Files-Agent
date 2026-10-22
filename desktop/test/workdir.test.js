'use strict'
const test = require('node:test')
const assert = require('node:assert')
const fs = require('fs')
const os = require('os')
const path = require('path')
const { validateDir, copyVerify, switchWorkdir } = require('../src/workdir')

const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), 'oa-wd-'))
const win = process.platform === 'win32'

test('validateDir rejects relative, traversal, root, install and data dirs', async () => {
  const base = tmp()
  assert.ok(!(await validateDir('relative\\dir', {})).ok)
  assert.ok(!(await validateDir('', {})).ok)
  const inst = path.join(base, 'install')
  fs.mkdirSync(inst)
  assert.ok(!(await validateDir(path.join(inst, 'archive'), { installDirs: [inst], kind: 'archive' })).ok)
  const data = path.join(base, 'data')
  fs.mkdirSync(data)
  assert.ok(!(await validateDir(path.join(data, 'a'), { dataDir: data, kind: 'archive' })).ok)
  assert.ok(!(await validateDir(base + path.sep + 'x' + path.sep + '..' + path.sep + 'y', {})).ok)
  assert.ok(!(await validateDir(path.parse(base).root, {})).ok)
  assert.ok(!(await validateDir(path.join(base, 'sys', 'x'), { protectedDirs: [path.join(base, 'sys')] })).ok)
  assert.ok(!(await validateDir(base, { exactProtected: [base] })).ok)
  const ok = await validateDir(path.join(base, 'good', 'archive'), { installDirs: [inst], dataDir: data, kind: 'archive' })
  assert.ok(ok.ok, JSON.stringify(ok))
  assert.ok(ok.freeBytes === null || ok.freeBytes > 0)
  fs.rmSync(base, { recursive: true, force: true })
})

test('copyVerify copies everything, verifies and never touches the source', async () => {
  const base = tmp()
  const src = path.join(base, 'src')
  fs.mkdirSync(path.join(src, 'a', 'b'), { recursive: true })
  fs.mkdirSync(path.join(src, 'empty'))
  fs.writeFileSync(path.join(src, 'x.txt'), 'hello')
  fs.writeFileSync(path.join(src, 'a', 'b', '合同.pdf'), Buffer.alloc(100000, 7))
  const seen = []
  const r = await copyVerify(src, path.join(base, 'dst'), { onProgress: (p) => seen.push(p) })
  assert.strictEqual(r.files, 2)
  assert.strictEqual(fs.readFileSync(path.join(base, 'dst', 'x.txt'), 'utf8'), 'hello')
  assert.strictEqual(fs.statSync(path.join(base, 'dst', 'a', 'b', '合同.pdf')).size, 100000)
  assert.ok(fs.existsSync(path.join(base, 'dst', 'empty')))
  assert.strictEqual(seen.at(-1).files, 2)
  assert.ok(fs.existsSync(path.join(src, 'x.txt')))
  // non-empty target / nesting are refused
  await assert.rejects(copyVerify(src, path.join(base, 'dst')), /不是空的/)
  await assert.rejects(copyVerify(src, path.join(src, 'inside')), /互相包含/)
  fs.rmSync(base, { recursive: true, force: true })
})

function harness({ startResults, copyFails }) {
  const calls = []
  let root = 'OLD'
  const results = [...startResults]
  return {
    calls,
    get root() { return root },
    args: {
      oldRoot: 'OLD', newRoot: 'NEW', mode: 'migrate',
      stopBackend: async () => calls.push('stop'),
      copy: async () => { calls.push('copy'); if (copyFails) throw new Error('disk error') },
      setRoot: (p) => { root = p; calls.push('set:' + p) },
      startBackend: async () => { calls.push('start:' + root); return results.shift() },
    },
  }
}

test('switchWorkdir success: copy then switch then start on the new root', async () => {
  const h = harness({ startResults: [true] })
  const r = await switchWorkdir(h.args)
  assert.ok(r.ok)
  assert.deepStrictEqual(h.calls, ['stop', 'copy', 'set:NEW', 'start:NEW'])
  assert.strictEqual(h.root, 'NEW')
})

test('switchWorkdir: copy failure never switches and restarts on the old root', async () => {
  const h = harness({ startResults: [true], copyFails: true })
  const r = await switchWorkdir(h.args)
  assert.ok(!r.ok && r.stage === 'copy' && r.restored)
  assert.strictEqual(h.root, 'OLD')
  assert.ok(!h.calls.includes('set:NEW'))
})

test('switchWorkdir: failed start on the new root rolls back to the old root', async () => {
  const h = harness({ startResults: [false, true] })
  const r = await switchWorkdir(h.args)
  assert.ok(!r.ok && r.stage === 'start' && r.restored)
  assert.strictEqual(h.root, 'OLD')
  assert.deepStrictEqual(h.calls, ['stop', 'copy', 'set:NEW', 'start:NEW', 'set:OLD', 'start:OLD'])
})

test('switchWorkdir mode use skips the copy', async () => {
  const h = harness({ startResults: [true] })
  await switchWorkdir({ ...h.args, mode: 'use' })
  assert.ok(!h.calls.includes('copy'))
})

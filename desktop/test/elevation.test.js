'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const { isElevated } = require('../src/elevation')

test('not Windows: never reports elevation', async () => {
  assert.equal(await isElevated({ platform: 'linux' }), false)
})
test('Windows: fltmc success means elevated', async () => {
  let called
  assert.equal(await isElevated({ platform: 'win32', execFile: (exe, args, o, cb) => { called = exe; cb(null) } }), true)
  assert.match(called, /fltmc\.exe$/i)
})
test('Windows: access denied means not elevated', async () => {
  assert.equal(await isElevated({ platform: 'win32', execFile: (_e, _a, _o, cb) => cb(Object.assign(new Error('denied'), { code: 5 })) }), false)
})
test('Windows: exec throwing means unknown, reported as not elevated', async () => {
  assert.equal(await isElevated({ platform: 'win32', execFile: () => { throw new Error('boom') } }), false)
})

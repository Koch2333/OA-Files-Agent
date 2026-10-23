'use strict'
const test = require('node:test')
const assert = require('node:assert')
const { normalizeOption, fmtBytes } = require('../src/settings-model')

test('only known options with valid values are accepted', () => {
  assert.strictEqual(normalizeOption('autoStart', true), true)
  assert.strictEqual(normalizeOption('minimizeToTray', false), false)
  assert.throws(() => normalizeOption('autoStart', 'yes'), /开|关/)
  assert.throws(() => normalizeOption('archiveRoot', 'C:\\x'), /不支持/) // the folder is changed through its own guarded flow
  assert.throws(() => normalizeOption('__proto__', true), /不支持/)
})

test('port: 0/empty = automatic, otherwise 1024-65535', () => {
  assert.strictEqual(normalizeOption('fixedPort', ''), 0)
  assert.strictEqual(normalizeOption('fixedPort', null), 0)
  assert.strictEqual(normalizeOption('fixedPort', 0), 0)
  assert.strictEqual(normalizeOption('fixedPort', '18080'), 18080)
  assert.throws(() => normalizeOption('fixedPort', 80), /1024/)
  assert.throws(() => normalizeOption('fixedPort', 70000), /1024/)
  assert.throws(() => normalizeOption('fixedPort', 12.5), /整数/)
  assert.throws(() => normalizeOption('fixedPort', 'abc'), /整数/)
})

test('fmtBytes', () => {
  assert.strictEqual(fmtBytes(5 * 1024 ** 3), '5.0 GB')
  assert.strictEqual(fmtBytes(300 * 1024 ** 2), '300 MB')
  assert.strictEqual(fmtBytes(null), '')
})

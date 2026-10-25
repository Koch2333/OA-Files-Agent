'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const { dialogOptions, pickedPaths, pickPaths } = require('../src/pick-paths')

test('file mode: open files, multi-select, supported-format filter first', () => {
  const o = dialogOptions({ folders: false })
  assert.deepEqual(o.properties, ['openFile', 'multiSelections'])
  assert.ok(o.filters[0].extensions.includes('pdf'))
})
test('folder mode only when folders === true exactly; junk input means file mode', () => {
  assert.deepEqual(dialogOptions({ folders: true }).properties, ['openDirectory', 'multiSelections'])
  assert.deepEqual(dialogOptions({ folders: 'yes' }).properties, ['openFile', 'multiSelections'])
  assert.deepEqual(dialogOptions(undefined).properties, ['openFile', 'multiSelections'])
  assert.deepEqual(dialogOptions({ properties: ['showHiddenFiles'] }).properties, ['openFile', 'multiSelections']) // renderer cannot inject dialog properties
})
test('cancelled / malformed results give []', () => {
  assert.deepEqual(pickedPaths({ canceled: true, filePaths: ['/a'] }), [])
  assert.deepEqual(pickedPaths(null), [])
  assert.deepEqual(pickedPaths({ canceled: false }), [])
})
test('only absolute, NUL-free strings survive', () => {
  const abs = process.platform === 'win32' ? 'C:\\data\\a.pdf' : '/data/a.pdf'
  assert.deepEqual(pickedPaths({ canceled: false, filePaths: [abs, 'relative.pdf', '', 5, `${abs}\0x`] }), [abs])
})
test('pickPaths wires the dialog', async () => {
  let seen
  const abs = process.platform === 'win32' ? 'C:\\x' : '/x'
  const r = await pickPaths(async (o) => { seen = o; return { canceled: false, filePaths: [abs] } }, { folders: true })
  assert.deepEqual(r, [abs])
  assert.deepEqual(seen.properties, ['openDirectory', 'multiSelections'])
})

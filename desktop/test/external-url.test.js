'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const { checkExternalUrl } = require('../src/external-url')

test('allows https URLs on allow-listed hosts', () => {
  assert.equal(checkExternalUrl('https://openrouter.ai/auth?callback_url=http%3A%2F%2F127.0.0.1%3A1%2Fx'), 'https://openrouter.ai/auth?callback_url=http%3A%2F%2F127.0.0.1%3A1%2Fx')
})

test('refuses everything else', () => {
  for (const u of ['http://openrouter.ai/auth', 'https://evil.example/auth', 'https://openrouter.ai.evil.example/', 'https://user:pw@openrouter.ai/', 'https://openrouter.ai:8443/', 'file:///C:/Windows/System32/calc.exe', 'javascript:alert(1)', 'ms-msdt:/x', '', null, 5, 'not a url']) {
    assert.equal(checkExternalUrl(u), null, String(u))
  }
})

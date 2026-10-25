'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const { checkExternalUrl, ALLOWED_HOSTS } = require('../src/external-url')

test('allows https URLs on allow-listed hosts', () => {
  assert.equal(checkExternalUrl('https://openrouter.ai/auth?callback_url=http%3A%2F%2F127.0.0.1%3A1%2Fx'), 'https://openrouter.ai/auth?callback_url=http%3A%2F%2F127.0.0.1%3A1%2Fx')
})

test('refuses everything else', () => {
  for (const u of ['http://openrouter.ai/auth', 'https://evil.example/auth', 'https://openrouter.ai.evil.example/', 'https://user:pw@openrouter.ai/', 'https://openrouter.ai:8443/', 'file:///C:/Windows/System32/calc.exe', 'javascript:alert(1)', 'ms-msdt:/x', '', null, 5, 'not a url']) {
    assert.equal(checkExternalUrl(u), null, String(u))
  }
})

test('allow-list equals the hosts of the shipped presets key_url (plus the OpenRouter login host)', () => {
  const presets = JSON.parse(fs.readFileSync(path.join(__dirname, '..', '..', 'backend', 'internal', 'llm', 'presets.json'), 'utf8'))
  const hosts = new Set(['openrouter.ai'])
  for (const p of presets) {
    if (!p.key_url) continue
    const u = new URL(p.key_url)
    assert.equal(u.protocol, 'https:', p.id)
    assert.equal(checkExternalUrl(p.key_url), u.href, p.id)
    hosts.add(u.hostname)
  }
  assert.deepEqual([...ALLOWED_HOSTS].sort(), [...hosts].sort())
})

test('key pages: only exact hosts, no look-alikes', () => {
  assert.ok(checkExternalUrl('https://platform.deepseek.com/api_keys'))
  assert.ok(checkExternalUrl('https://platform.openai.com/api-keys'))
  assert.ok(checkExternalUrl('https://bailian.console.aliyun.com/cn-beijing/model/settings/api-key'))
  for (const u of ['https://deepseek.com/api_keys', 'https://platform.deepseek.com.evil.example/', 'http://platform.openai.com/api-keys', 'https://console.aliyun.com/', 'https://api.openai.com/v1']) {
    assert.equal(checkExternalUrl(u), null, u)
  }
})

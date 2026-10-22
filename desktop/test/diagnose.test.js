'use strict'
const test = require('node:test')
const assert = require('node:assert')
const { diagnose } = require('../src/diagnose')
const { cleanEnv } = require('../src/backend')

test('diagnose recognises common startup failures', () => {
  assert.match(diagnose('listen tcp 127.0.0.1:8080: bind: Only one usage of each socket address').title, /端口/)
  assert.match(diagnose('fork/exec oa-agent.exe: Operation did not complete successfully because the file contains a virus').title, /安全软件/)
  assert.match(diagnose('pg_ctl start failed: could not locate a valid checkpoint record').title, /数据库/)
  assert.match(diagnose('worker executable not found: x').title, /不完整/)
  assert.match(diagnose('whatever').title, /启动失败/)
})

test('cleanEnv drops unrelated secrets but keeps what the backend needs', () => {
  process.env.AWS_SECRET_ACCESS_KEY = 'x'
  process.env.OA_DATABASE_URL = 'postgres://secret'
  process.env.OA_LOG_LEVEL = 'info'
  const e = cleanEnv({ OA_STANDALONE: '1' })
  assert.strictEqual(e.AWS_SECRET_ACCESS_KEY, undefined)
  assert.strictEqual(e.OA_DATABASE_URL, undefined)
  assert.strictEqual(e.OA_LOG_LEVEL, 'info')
  assert.strictEqual(e.OA_STANDALONE, '1')
})

// Minimal OpenAI-compatible fake for the browser e2e: node e2e/lib/fake-llm.mjs [port]  (default 18090)
//   GET  /v1/models            -> two model ids
//   POST /v1/chat/completions  -> tool-call probe answers with an `echo` tool call; document analysis decides from the FILE NAME only:
//                                 names containing "合同" are fully determined (采购部 / 合同 / 2026-03-15), everything else is undetermined.
// It never needs OCR and never sees real documents. Logs one line per document call (file name + whether an instruction block was present).
import http from 'node:http'

const port = Number(process.argv[2] ?? 18090)
const server = http.createServer((req, res) => {
  const send = (code, body) => { res.writeHead(code, { 'content-type': 'application/json' }); res.end(JSON.stringify(body)) }
  if (req.method === 'GET' && req.url.startsWith('/v1/models')) return send(200, { data: [{ id: 'fake-model' }, { id: 'fake-model-2' }] })
  if (req.method === 'POST' && req.url.startsWith('/v1/chat/completions')) {
    let raw = ''
    req.on('data', (c) => { raw += c })
    req.on('end', () => {
      let body = {}
      try { body = JSON.parse(raw) } catch { /* ignore */ }
      const msgs = body.messages ?? []
      const system = msgs.find((m) => m.role === 'system')?.content ?? ''
      if ((body.tools ?? []).length > 0 && system.includes('echo tool')) {
        return send(200, { choices: [{ message: { content: null, tool_calls: [{ id: 'c1', type: 'function', function: { name: 'echo', arguments: '{"text":"ok"}' } }] } }] })
      }
      let answer = { department: null, document_type: null, document_date: null, evidence: [], review_required: true }
      const user = msgs[1]?.content ?? ''
      if (user.includes('DOCUMENT_DATA')) {
        const i = user.indexOf('>>>\n')
        const name = i >= 0 ? user.slice(i + 4).split('\n')[0] : ''
        if (name.includes('合同')) answer = { department: '采购部', document_type: '合同', document_date: '2026-03-15', evidence: ['文件名含“合同”'], review_required: false }
        console.log(new Date().toISOString(), 'document call', name, system.includes('用户任务说明') ? '[instruction]' : '')
      }
      send(200, { choices: [{ message: { content: JSON.stringify(answer) } }] })
    })
    return
  }
  send(404, { error: 'not found' })
})
server.listen(port, '127.0.0.1', () => console.log('fake LLM on http://127.0.0.1:' + port + '/v1'))

import { API_BASE, fireSetupRequired, fireUnauthenticated, fireWizardRequired } from './http'
import type { AgentEvent } from './types'

/**
 * 任务事件 SSE。使用 fetch 流而非 EventSource：
 * 1) 会话 Cookie 同源携带，401/503 时与其他请求一样交给全局处理；2) 能精确控制 Last-Event-ID 续传与重连退避。
 * 协议见 contracts/events.md（草案，认证与保留策略待补）。
 */
export function openEventStream(
  taskId: string,
  onEvent: (e: AgentEvent) => void,
  onState: (s: 'open' | 'reconnecting' | 'closed') => void,
): () => void {
  const ctrl = new AbortController()
  let lastId = 0
  let stopped = false
  let delay = 1000

  async function run() {
    while (!stopped) {
      try {
        const res = await fetch(`${API_BASE}/v1/tasks/${encodeURIComponent(taskId)}/events`, {
          headers: { Accept: 'text/event-stream', ...(lastId ? { 'Last-Event-ID': String(lastId) } : {}) },
          signal: ctrl.signal,
          credentials: 'same-origin', // 会话 Cookie；SSE 是 GET，无需 CSRF 头
        })
        if (!res.ok || !res.body) {
          if (res.status === 401) { fireUnauthenticated(); onState('closed'); return }
          if (res.status === 503) { fireSetupRequired(); onState('closed'); return }
          if (res.status === 409) { fireWizardRequired(); onState('closed'); return }
          if (res.status === 400 || res.status === 403 || res.status === 404) { onState('closed'); return }
          throw new Error('bad status ' + res.status)
        }
        onState('open')
        delay = 1000
        const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
        let buf = ''
        for (;;) {
          const { value, done } = await reader.read()
          if (done) break
          buf += value
          let idx: number
          while ((idx = buf.search(/\r?\n\r?\n/)) >= 0) {
            const block = buf.slice(0, idx)
            buf = buf.slice(idx).replace(/^\r?\n\r?\n/, '')
            const ev = parseBlock(block)
            if (ev && ev.id > lastId) { lastId = ev.id; onEvent(ev) }
          }
        }
      } catch {
        if (stopped) return
      }
      if (stopped) return
      onState('reconnecting')
      await new Promise((r) => setTimeout(r, delay))
      delay = Math.min(delay * 2, 15000)
    }
  }
  void run()
  return () => { stopped = true; ctrl.abort(); onState('closed') }
}

export function parseBlock(block: string): AgentEvent | null {
  const data: string[] = []
  let id: string | undefined
  for (const line of block.split(/\r?\n/)) {
    if (line.startsWith(':')) continue // 心跳注释
    if (line.startsWith('id:')) id = line.slice(3).trim()
    else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
  }
  if (!data.length) return null
  try {
    const obj = JSON.parse(data.join('\n')) as AgentEvent
    if (id && /^\d+$/.test(id)) obj.id = Number(id)
    return typeof obj.id === 'number' ? obj : null
  } catch { return null }
}

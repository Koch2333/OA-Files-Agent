/** 周期任务的调度辅助：指数退避、标签页隐藏时暂停、仅在 enabled 时运行。纯逻辑，计时器可注入以便测试。 */

/** 第 failures 次连续失败后的等待时间：base * 2^failures，上限 max（failures 为 0 时就是 base）。 */
export function backoffDelay(base: number, failures: number, max = base * 16): number {
  if (failures <= 0) return base
  return Math.min(max, base * 2 ** Math.min(failures, 30))
}

export interface PollerOpts {
  /** 单次执行；抛出（或 reject）视为失败并退避 */
  run: () => Promise<unknown>
  intervalMs: number
  maxIntervalMs?: number
  /** 是否应当运行（例如：已登录且系统已初始化）。为 false 时不调度，也不执行 */
  enabled?: () => boolean
  /** 标签页是否隐藏；隐藏时暂停，恢复可见时立即补一次 */
  hidden?: () => boolean
  setTimer?: (fn: () => void, ms: number) => unknown
  clearTimer?: (h: unknown) => void
}

export interface Poller {
  start(): void
  stop(): void
  /** 立即执行一次（尊重 enabled / hidden），然后继续按间隔调度 */
  kick(): void
  /** 可见性或 enabled 条件变化后调用 */
  resume(): void
  failures(): number
  running(): boolean
}

export function createPoller(o: PollerOpts): Poller {
  const setT = o.setTimer ?? ((fn, ms) => setTimeout(fn, ms))
  const clearT = o.clearTimer ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>))
  const enabled = o.enabled ?? (() => true)
  const hidden = o.hidden ?? (() => false)
  let handle: unknown
  let active = false
  let failures = 0
  let inflight = false
  const clear = () => { if (handle !== undefined) { clearT(handle); handle = undefined } }
  function schedule() {
    clear()
    if (!active) return
    handle = setT(() => { handle = undefined; void tick() }, backoffDelay(o.intervalMs, failures, o.maxIntervalMs))
  }
  async function tick() {
    if (!active) return
    // 条件不满足时不发请求，只是继续按间隔检查（隐藏时由 resume 立即补跑）
    if (!enabled() || hidden()) { schedule(); return }
    if (inflight) { schedule(); return }
    inflight = true
    try { await o.run(); failures = 0 } catch { failures++ } finally { inflight = false }
    schedule()
  }
  return {
    start() { if (active) return; active = true; failures = 0; schedule() },
    stop() { active = false; clear() },
    kick() { if (!active) return; clear(); void tick() },
    resume() { if (!active || !enabled() || hidden()) return; clear(); void tick() },
    failures: () => failures,
    running: () => active,
  }
}

/** 当前正在运行的轮询器；标签页重新可见 / 浏览器恢复联网时统一补跑一次（隐藏期间它们都是暂停的）。 */
const live = new Set<Poller>()
export function resumeAllPollers() { for (const p of live) p.resume() }
export const isHidden = () => typeof document !== 'undefined' && document.hidden
/** 与 createPoller 相同，但登记到 resumeAllPollers，并默认在标签页隐藏时暂停。 */
export function createManagedPoller(o: PollerOpts): Poller {
  const p = createPoller({ hidden: isHidden, ...o })
  return {
    ...p,
    start() { live.add(p); p.start() },
    stop() { live.delete(p); p.stop() },
  }
}

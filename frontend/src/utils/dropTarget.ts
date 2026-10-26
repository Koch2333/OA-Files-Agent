import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'
import type { PickedFile } from '../api/types'
import { isReadable } from './formats'

/**
 * 页面级文件拖放：整页都是投放区（带“松开即可上传”遮罩），页面外的拖放永不导航离开应用。
 * 处理逻辑是不依赖 Vue/DOM 的纯函数（createDropHandlers），便于用模拟事件做单元测试；
 * 与上传机制无关：投放结果只交给一个回调 onDrop(dataTransfer)。
 */

export interface DropEventLike {
  dataTransfer?: { types?: ArrayLike<string> | readonly string[]; dropEffect?: string } | null
  preventDefault(): void
}
export const hasFiles = (e: DropEventLike) => Array.from(e.dataTransfer?.types ?? []).includes('Files')

/** 已被某个投放区处理的事件；全局守卫据此判断“落在了不能放的地方”。 */
const handled = new WeakSet<object>()
export const isHandled = (e: object) => handled.has(e)

export interface DropOptions {
  /** 当前是否接受文件（只读账号、正在提交时为 false：仍阻止导航，但不显示投放态）。 */
  enabled: () => boolean
  onDrop: (dt: DataTransfer) => void
  setDragging: (on: boolean) => void
}

export function createDropHandlers(o: DropOptions) {
  let depth = 0
  const mark = (e: DropEventLike) => { handled.add(e); e.preventDefault() }
  return {
    enter(e: DropEventLike) { if (!hasFiles(e)) return; mark(e); depth++; if (o.enabled()) o.setDragging(true) },
    over(e: DropEventLike) {
      if (!hasFiles(e)) return
      mark(e)
      if (e.dataTransfer) e.dataTransfer.dropEffect = o.enabled() ? 'copy' : 'none'
      if (o.enabled()) o.setDragging(true)
    },
    leave(e: DropEventLike) { if (!hasFiles(e)) return; depth = Math.max(0, depth - 1); if (depth === 0) o.setDragging(false) },
    drop(e: DropEventLike) {
      if (!hasFiles(e)) return
      mark(e); depth = 0; o.setDragging(false)
      if (o.enabled() && e.dataTransfer) o.onDrop(e.dataTransfer as unknown as DataTransfer)
    },
  }
}

/** 在 window 上注册页面级投放（挂载时注册，卸载时移除）。返回 dragging 供遮罩/高亮使用。 */
export function useWindowDrop(opts: { enabled: () => boolean; onDrop: (dt: DataTransfer) => void }): { dragging: Ref<boolean> } {
  const dragging = ref(false)
  const h = createDropHandlers({ ...opts, setDragging: (v) => { dragging.value = v } })
  const on = (add: boolean) => {
    const f = add ? window.addEventListener.bind(window) : window.removeEventListener.bind(window)
    f('dragenter', h.enter as EventListener); f('dragover', h.over as EventListener); f('dragleave', h.leave as EventListener); f('drop', h.drop as EventListener)
  }
  onMounted(() => on(true))
  onBeforeUnmount(() => { on(false); dragging.value = false })
  return { dragging }
}

/**
 * 全局守卫（main.ts 安装一次）：任何页面上拖入文件都 preventDefault，避免浏览器/Electron 把文件当作导航目标打开
 * （会让整个界面被图片/PDF 取代）；没有投放区接手时给出提示。
 */
export function installDropGuard(target: Pick<Window, 'addEventListener'>, hint: (text: string) => void) {
  target.addEventListener('dragover', ((e: DragEvent) => { if (!hasFiles(e)) return; e.preventDefault(); if (!isHandled(e) && e.dataTransfer) e.dataTransfer.dropEffect = 'none' }) as EventListener)
  target.addEventListener('drop', ((e: DragEvent) => {
    if (!hasFiles(e)) return
    e.preventDefault()
    // 守卫先于页面的投放区监听器运行，所以延后一拍再看是否被接手
    setTimeout(() => { if (!isHandled(e)) hint('这里不能放文件。请到“智能归档”页面拖入，或点“选择文件”。') }, 0)
  }) as EventListener)
}

/** 投放结果提示（不涉及任何大小/数量限制）。 */
export interface IntakeNote { text: string; kind: 'info' | 'error' | 'success' }
export function intakeNote(added: PickedFile[], skipped: string[]): IntakeNote {
  if (!added.length) {
    return { kind: 'error', text: skipped.length ? `没有读取到可用的文件：${skipped.length} 个条目读取失败或路径不安全。` : '没有读取到文件。拖入的可能是空文件夹、快捷方式，或没有读取权限的位置；也可以点“选择文件”。' }
  }
  const nameOnly = added.filter((p) => !isReadable(p.file.name) || p.file.size === 0).length
  const parts = [`已添加 ${added.length} 个文件`]
  if (nameOnly) parts.push(`${nameOnly} 个文件的内容无法读取，将只按文件名处理（仍会提交，进入人工审核）`)
  if (skipped.length) parts.push(`${skipped.length} 个条目已跳过`)
  const bad = false
  return { kind: bad ? 'error' : parts.length > 1 ? 'info' : 'success', text: parts.join('；') + '。' }
}

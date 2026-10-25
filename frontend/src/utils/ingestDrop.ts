import type { Ref } from 'vue'
import type { PickedFile } from '../api/types'
import { errMsg } from './errorInfo'
import { collectDropped } from './paths'
import { intakeNote, type IntakeNote } from './dropTarget'

/**
 * 单一入口：把投放的 DataTransfer 变成待导入文件并通知调用方。与上传机制无关——
 * 调用方（useImportFlow / FilePicker）拿到的就是 File 对象与相对路径，之后交给上传器。
 */
export async function ingestDrop(dt: DataTransfer, o: { add: (l: PickedFile[]) => void; notify: (n: IntakeNote) => void; scanning: Ref<boolean>; found?: Ref<number>; err?: Ref<string | null> }) {
  o.scanning.value = true; if (o.found) o.found.value = 0; if (o.err) o.err.value = null
  const skipped: string[] = []
  try {
    const list = await collectDropped(dt, { skipped, onProgress: (n) => { if (o.found) o.found.value = n } })
    o.add(list)
    o.notify(intakeNote(list, skipped))
  } catch (x) {
    const m = '读取拖入内容失败：' + errMsg(x)
    if (o.err) o.err.value = m
    o.notify({ kind: 'error', text: m })
  } finally { o.scanning.value = false }
}

import type { ImportOptions, PickedFile } from './types'

/**
 * 构造 POST /v1/imports 的 multipart 正文。
 * 契约：paths 必须最先出现且仅一次；若带 options，必须紧跟 paths、位于第一个 files 之前，且仅一次；随后 files 与 paths 同序。
 */
export function buildImportForm(files: PickedFile[], options?: ImportOptions): FormData {
  const fd = new FormData()
  fd.append('paths', JSON.stringify(files.map((f) => f.relativePath)))
  if (options && Object.keys(options).length) fd.append('options', JSON.stringify(options))
  for (const f of files) fd.append('files', f.file, f.file.name)
  return fd
}

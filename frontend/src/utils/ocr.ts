import type { FileRecord } from '../api/types'

export type OcrInfo = NonNullable<FileRecord['ocr']>

const QUALITY: Record<string, string> = { good: '好', fair: '一般', poor: '差' }
const PROFILE: Record<string, string> = {
  default: '默认', enhanced: '增强', rotate: '自动旋转', deskew: '纠偏', high_dpi: '高清渲染', binarize: '二值化', vision: '视觉模型',
}

export const qualityLabel = (q: OcrInfo['quality'] | undefined): string => (q ? QUALITY[q] : '未评估')
export const qualityClass = (q: OcrInfo['quality'] | undefined): 'success' | 'attention' | 'error' | '' => (q === 'good' ? 'success' : q === 'fair' ? 'attention' : q === 'poor' ? 'error' : '')
export const profileLabel = (p: string): string => PROFILE[p] ?? p
export const confidencePct = (c: number | null | undefined): string => (c == null ? '—' : `${Math.round(c * 100)}%`)
export const isPoorOcr = (f: Pick<FileRecord, 'ocr'>): boolean => f.ocr?.used === true && f.ocr.quality === 'poor'

/** 演示用文档预览：按扩展名生成结构化内容；PDF/图片的页图是内联 SVG，演示模式下也能直接看到。 */
import type { Preview } from '../api/types'
import { formatOf } from '../utils/formats'

const ext = (name: string) => name.split('.').pop()?.toLowerCase() ?? ''
const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]!)

const PAGE_LINES = ['（演示数据）这是示例页面，用于演示预览窗口的翻页与缩放。', '真实环境中这里显示原件对应页的渲染图。', '部门：采购部    文种：合同    日期：2026-03-12']

/** 画一页带文字的 SVG（data URL）；max_side 决定像素尺寸，缩略图用更小的字号保持可读。 */
export function mockPageUrl(name: string, page: number, maxSide: number, pages = 3): string {
  const w = Math.max(120, Math.min(maxSide, 1400)), h = Math.round(w * 1.414)
  const k = w / 800
  const fs = (n: number) => Math.round(n * k)
  const lines = PAGE_LINES.map((l, i) => `<text x="${fs(60)}" y="${fs(220 + i * 44)}" font-size="${fs(22)}" fill="#333">${esc(l)}</text>`).join('')
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}"><rect width="100%" height="100%" fill="#fff"/><rect x="1" y="1" width="${w - 2}" height="${h - 2}" fill="none" stroke="#bbb"/>`
    + `<text x="${fs(60)}" y="${fs(100)}" font-size="${fs(34)}" font-weight="bold" fill="#111">${esc(name)}</text>`
    + `<text x="${fs(60)}" y="${fs(150)}" font-size="${fs(24)}" fill="#555">第 ${page} 页 / 共 ${pages} 页</text>${lines}`
    + `<text x="${w / 2}" y="${h - fs(40)}" font-size="${fs(20)}" text-anchor="middle" fill="#999">- ${page} -</text></svg>`
  return 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg)
}

const base = (name: string, size: number, over: Partial<Preview>): Preview => ({
  kind: 'unsupported', name, mime_type: 'application/octet-stream', size_bytes: size, pages: null, page_url: null, message: '', truncated: false, warnings: [], ...over,
})

const sheetRows = (n: number, cols: string[]) => [cols, ...Array.from({ length: n - 1 }, (_, i) => cols.map((c, j) => (j === 0 ? String(i + 1) : j === 1 ? `项目${i + 1}` : String(((i + 3) * (j + 7)) % 1000))))]

export function mockPreview(name: string, size: number): Preview {
  const e = ext(name)
  if (e === 'pdf') return base(name, size, { kind: 'pdf', mime_type: 'application/pdf', pages: 3, page_url: '/mock/preview/page?page={page}' })
  const fmt = formatOf(name)
  if (fmt?.group === 'image') return base(name, size, { kind: 'image', mime_type: fmt.mime, pages: e === 'tif' || e === 'tiff' ? 2 : 1, page_url: '/mock/preview/page?page={page}' })
  if (fmt && (fmt.group === 'word' || fmt.group === 'powerpoint' || fmt.group === 'text' || fmt.group === 'web' || fmt.group === 'mail') && e !== 'docx') {
    const slides = fmt.group === 'powerpoint'
    return base(name, size, {
      kind: 'document', mime_type: fmt.mime,
      warnings: fmt.legacy ? ['旧版 Office 文件由内置解析器读取，版式与图片不会显示（演示数据）'] : [],
      blocks: slides
        ? [{ type: 'heading', level: 2, text: '幻灯片 1' }, { type: 'heading', level: 3, text: '2026 年度工作汇报' }, { type: 'paragraph', align: 'left', runs: [{ text: '第一点：采购进度' }] }, { type: 'heading', level: 2, text: '幻灯片 2' }, { type: 'paragraph', align: 'left', runs: [{ text: '备注：讲稿内容（演示数据）' }] }]
        : [{ type: 'heading', level: 1, text: '关于采购设备的通知' }, { type: 'paragraph', align: 'left', runs: [{ text: '（演示数据）这是由 ' }, { text: fmt.label, bold: true }, { text: ' 解析出的文本，旧版格式与新格式使用同一套结构化预览。' }] }, { type: 'table', rows: [['项目', '数量'], ['笔记本电脑', '20']] }],
    })
  }
  if (fmt?.preview === 'spreadsheet' && e !== 'xlsx') {
    return base(name, size, { kind: 'spreadsheet', mime_type: fmt.mime, sheets: [{ name: 'Sheet1', rows: sheetRows(8, ['序号', '项目', '数量', '单价']), rows_total: 8, cols_total: 4, truncated: false }] })
  }
  if (e === 'docx') {
    return base(name, size, {
      kind: 'document', mime_type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
      header: '某某集团有限公司 · 内部文件', footer: '第 1 页',
      warnings: ['含 2 张图片未显示'],
      blocks: [
        { type: 'heading', level: 1, text: '2026 年度采购合同' },
        { type: 'paragraph', align: 'center', runs: [{ text: '合同编号：', bold: true }, { text: 'CG-2026-0312' }] },
        { type: 'heading', level: 2, text: '一、合同主体' },
        { type: 'paragraph', runs: [{ text: '甲方：' }, { text: '某某集团有限公司', bold: true }, { text: '（以下简称“' }, { text: '采购方', italic: true }, { text: '”）' }] },
        { type: 'list_item', level: 0, runs: [{ text: '采购范围：办公设备与耗材' }] },
        { type: 'list_item', level: 1, runs: [{ text: '台式电脑 20 台', bold: true }] },
        { type: 'list_item', level: 1, runs: [{ text: '打印机 5 台' }] },
        { type: 'heading', level: 2, text: '二、价格明细' },
        { type: 'table', rows: [['品名', '数量', '单价（元）', '金额（元）'], ['台式电脑', '20', '4200', '84000'], ['打印机', '5', '1800', '9000']] },
        { type: 'paragraph', align: 'right', runs: [{ text: '2026 年 3 月 12 日' }] },
      ],
    })
  }
  if (e === 'xlsx') {
    return base(name, size, {
      kind: 'spreadsheet', mime_type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', truncated: true,
      sheets: [
        { name: '汇总', rows: sheetRows(12, ['序号', '项目', '一月', '二月', '三月']), rows_total: 12, cols_total: 5, truncated: false },
        { name: '明细', rows: sheetRows(200, ['序号', '项目', '数量', '单价', '金额', '备注']), rows_total: 1234, cols_total: 6, truncated: true },
        { name: '备注', rows: [['说明'], ['演示数据，仅用于展示预览效果。']], rows_total: 2, cols_total: 1, truncated: false },
      ],
    })
  }
  return base(name, size, { message: `暂不支持预览 .${e || '该'} 格式的文件，可下载后查看；文件仍可按文件名归档。` })
}

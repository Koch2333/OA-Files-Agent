import { argbFromHex, applyTheme, themeFromSourceColor } from '@material/material-color-utilities'

export type ThemeMode = 'system' | 'light' | 'dark'
export const SEEDS: { name: string; hex: string }[] = [
  { name: '紫罗兰', hex: '#6750A4' },
  { name: '海洋蓝', hex: '#1565C0' },
  { name: '松柏绿', hex: '#2E7D32' },
  { name: '琥珀橙', hex: '#C2570C' },
  { name: '玫瑰红', hex: '#B3265E' },
  { name: '石墨灰', hex: '#4A5A68' },
]

export function resolveDark(mode: ThemeMode): boolean {
  return mode === 'dark' || (mode === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
}

/** 由种子色生成 M3 配色并写入 :root 的 --md-sys-color-* 变量。 */
export function applyM3(seed: string, mode: ThemeMode) {
  const dark = resolveDark(mode)
  applyTheme(themeFromSourceColor(argbFromHex(seed)), { target: document.documentElement, dark })
  document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
  document.documentElement.dataset.dark = dark ? '1' : '0'
}

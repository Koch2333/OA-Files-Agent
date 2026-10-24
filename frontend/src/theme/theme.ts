import { argbFromHex, applyTheme, hexFromArgb, themeFromSourceColor } from '@material/material-color-utilities'

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

/** M3 surface roles that applyTheme() does not write: neutral-palette tones from the M3 spec. */
const SURFACE_TONES: Record<string, [light: number, dark: number]> = {
  'surface-dim': [87, 6],
  'surface-bright': [98, 24],
  'surface-container-lowest': [100, 4],
  'surface-container-low': [96, 10],
  'surface-container': [94, 12],
  'surface-container-high': [92, 17],
  'surface-container-highest': [90, 22],
}

/** 由种子色生成 M3 配色并写入 :root 的 --md-sys-color-* 变量（含 surface-container 系列）。 */
export function applyM3(seed: string, mode: ThemeMode) {
  const dark = resolveDark(mode)
  const theme = themeFromSourceColor(argbFromHex(seed))
  const root = document.documentElement
  applyTheme(theme, { target: root, dark })
  for (const [name, [l, d]] of Object.entries(SURFACE_TONES)) root.style.setProperty(`--md-sys-color-${name}`, hexFromArgb(theme.palettes.neutral.tone(dark ? d : l)))
  root.style.colorScheme = dark ? 'dark' : 'light'
  root.dataset.dark = dark ? '1' : '0'
}

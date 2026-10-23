// The Windows desktop edition (Electron) exposes a tiny bridge, window.oaDesktop (desktop/src/app-preload.js). In a normal
// browser, in the enterprise web app and in demo mode it does not exist and everything here returns null/false.
export interface DesktopInfo { version: string; archiveRoot: string; dataDir: string; portable: boolean }
export interface DesktopBridge {
  isDesktop: true
  info(): Promise<DesktopInfo | null>
  openSettings(): Promise<void>
  openFolder(what: 'archive' | 'logs'): Promise<void>
}

export function desktop(): DesktopBridge | null {
  const b = (globalThis as { oaDesktop?: DesktopBridge }).oaDesktop
  return b && b.isDesktop === true ? b : null
}

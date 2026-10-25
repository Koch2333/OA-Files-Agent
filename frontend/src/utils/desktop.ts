// The Windows desktop edition (Electron) exposes a tiny bridge, window.oaDesktop (desktop/src/app-preload.js). In a normal
// browser, in the enterprise web app and in demo mode it does not exist and everything here returns null/false.
export interface DesktopInfo { version: string; archiveRoot: string; dataDir: string; portable: boolean }
export type KeyHelperEvent =
  | { type: 'key'; provider: string; key: string; label?: string; clipboardCleared?: boolean }
  | { type: 'closed'; provider: string; delivered: boolean }
  /** the platform sent the user back to the app's loopback callback (sign-in view): the settings page's status polling finishes the connection */
  | { type: 'oauth-callback'; provider: string }
  /** the window moved on to the guide of this provider by itself (manage view → 替换密钥) */
  | { type: 'opened'; provider: string }
  /** manage view: the user chose 替换密钥 for a provider that signs in with its platform account: start that sign-in */
  | { type: 'replace'; provider: string }
  /** manage view: the user chose 换一个服务商 */
  | { type: 'switch-provider' }
  /** manage view asks the settings page (it holds the session) for the saved key's status, a connection test, or removal */
  | { type: 'request'; id: number; op: 'status' | 'test' | 'remove' }
export interface DesktopBridge {
  isDesktop: true
  info(): Promise<DesktopInfo | null>
  /** Running as administrator? (drag-and-drop from Explorer does not work then); older shells lack it. */
  elevated?(): Promise<boolean>
  openSettings(): Promise<void>
  openFolder(what: 'archive' | 'logs'): Promise<void>
  /** Opens a provider login page in the system browser (https + allow-listed host only, checked in the main process); older shells lack it. */
  openExternal?(url: string): Promise<boolean>
  /** Guided key-creation window (desktop edition): resolves { ok, message? }; older shells lack it. */
  openKeyHelper?(providerId: string, opts?: { mode?: 'create' | 'login' | 'manage'; authorizeUrl?: string }): Promise<{ ok: boolean; message?: string }>
  /** Answer to a { type: 'request' } event. */
  keyHelperReply?(id: number, data: unknown): Promise<boolean>
  /** Events of that window; returns an unsubscribe function. The key only arrives after the user confirmed it in the window. */
  onKeyHelper?(cb: (e: KeyHelperEvent) => void): () => void
  /** Absolute local paths of dropped/selected File objects (webUtils.getPathForFile; synchronous, "" when a File has no path); older shells lack it. */
  pathsForFiles?(files: ArrayLike<File>): string[]
  /** Native file/folder chooser; returns absolute paths ([] when cancelled); older shells lack it. */
  pickPaths?(opts?: { folders?: boolean }): Promise<string[]>
}

export function desktop(): DesktopBridge | null {
  const b = (globalThis as { oaDesktop?: DesktopBridge }).oaDesktop
  return b && b.isDesktop === true ? b : null
}

/** WebAuthn 可用性与错误文案（纯函数；浏览器调用封装在 src/utils/passkey.ts）。 */
export type Ceremony = 'register' | 'login' | 'reauth'

export interface Availability { secureContext: boolean; supported: boolean; serverAvailable?: boolean }

/** 返回 null 表示可用；否则返回原因（用于界面解释）。 */
export function passkeyUnavailableReason(a: Availability): string | null {
  if (!a.secureContext) return '当前页面不是安全连接（需要 HTTPS，或通过 localhost 访问），浏览器不允许使用 Passkey。'
  if (!a.supported) return '当前浏览器不支持 Passkey（WebAuthn）。'
  if (a.serverAvailable === false) return '服务器当前的访问域名/协议不满足 Passkey 要求（需要 HTTPS 或 localhost）。'
  return null
}

interface ErrLike { name?: string; code?: string; message?: string; cause?: { name?: string } }

const CODE_MESSAGES: Record<string, string> = {
  ERROR_CEREMONY_ABORTED: '操作已取消。',
  ERROR_AUTHENTICATOR_PREVIOUSLY_REGISTERED: '该验证器上已经注册过此账号的 Passkey，请换一个设备或删除旧的后再添加。',
  ERROR_INVALID_RP_ID: '当前域名与服务器配置的 RP ID 不匹配，请使用管理员配置的访问地址。',
  ERROR_INVALID_DOMAIN: '当前域名不是有效的 WebAuthn 域名，Passkey 需要 HTTPS 或 localhost。',
  ERROR_PASSTHROUGH_SEE_CAUSE_PROPERTY: '',
}

export function webauthnErrorMessage(e: unknown, ceremony: Ceremony = 'login'): string {
  const err = (e ?? {}) as ErrLike
  const name = err.cause?.name ?? err.name ?? ''
  if (err.code && CODE_MESSAGES[err.code]) return CODE_MESSAGES[err.code]
  switch (name) {
    case 'NotAllowedError':
      return ceremony === 'register'
        ? '注册已取消或超时。请重试，并在系统弹窗中完成验证（指纹、面容、PIN 或安全密钥）。'
        : '验证已取消、超时，或此设备上没有可用的 Passkey。'
    case 'InvalidStateError':
      return ceremony === 'register' ? '该验证器上已经注册过此账号的 Passkey。' : '验证器当前状态不可用，请重试。'
    case 'SecurityError':
      return '浏览器拒绝了请求：当前域名与服务器配置的 RP ID 不匹配，或页面不是安全连接。'
    case 'NotSupportedError':
      return '此设备或浏览器不支持所需的 Passkey 算法或功能。'
    case 'AbortError':
      return '操作已取消。'
    case 'ConstraintError':
      return '此验证器不满足要求（需要支持用户验证的平台或安全密钥）。'
    case 'UnknownError':
      return '验证器返回了未知错误，请重试。'
  }
  return err.message ? `Passkey 操作失败：${err.message}` : 'Passkey 操作失败，请重试。'
}

/** 用户主动取消不需要报错弹窗（只显示轻提示）。 */
export function isUserCancel(e: unknown): boolean {
  const err = (e ?? {}) as ErrLike
  return (err.cause?.name ?? err.name) === 'NotAllowedError' || err.code === 'ERROR_CEREMONY_ABORTED' || err.name === 'AbortError'
}

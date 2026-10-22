/**
 * 浏览器 WebAuthn 调用封装（@simplewebauthn/browser）。服务端选项/响应使用标准 JSON 形式，
 * 本模块只负责调用浏览器 API。演示模式（api.capabilities.webauthn=false）不触碰浏览器，返回占位凭据。
 */
import { browserSupportsWebAuthn, browserSupportsWebAuthnAutofill, startAuthentication, startRegistration, WebAuthnAbortService } from '@simplewebauthn/browser'
import type { PublicKeyCredentialCreationOptionsJSON, PublicKeyCredentialRequestOptionsJSON } from '@simplewebauthn/browser'
import { api, type WebAuthnJSON } from '../api'
import { passkeyUnavailableReason } from './webauthn'

export function passkeyAvailability(serverAvailable?: boolean): { ok: boolean; reason: string | null } {
  if (!api.capabilities.webauthn) return { ok: true, reason: null } // 演示：模拟
  const reason = passkeyUnavailableReason({ secureContext: typeof window !== 'undefined' && window.isSecureContext, supported: browserSupportsWebAuthn(), serverAvailable })
  return { ok: reason === null, reason }
}

const MOCK: WebAuthnJSON = { id: 'mock', rawId: 'mock', type: 'public-key', response: {}, mock: true }

export async function createPasskey(options: WebAuthnJSON): Promise<WebAuthnJSON> {
  if (!api.capabilities.webauthn) return MOCK
  return (await startRegistration({ optionsJSON: options as unknown as PublicKeyCredentialCreationOptionsJSON })) as unknown as WebAuthnJSON
}

export async function assertPasskey(options: WebAuthnJSON, opts: { autofill?: boolean } = {}): Promise<WebAuthnJSON> {
  if (!api.capabilities.webauthn) return MOCK
  return (await startAuthentication({ optionsJSON: options as unknown as PublicKeyCredentialRequestOptionsJSON, useBrowserAutofill: opts.autofill })) as unknown as WebAuthnJSON
}

export async function autofillSupported(): Promise<boolean> {
  if (!api.capabilities.webauthn) return false
  try { return await browserSupportsWebAuthnAutofill() } catch { return false }
}

/** 取消正在进行的（条件式）WebAuthn 请求，例如离开页面时。 */
export function cancelPasskeyCeremony() { try { WebAuthnAbortService.cancelCeremony() } catch { /* ignore */ } }

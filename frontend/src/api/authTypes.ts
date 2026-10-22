/** 登录与账号相关类型（全部来自契约 auth / setup / admin-users）。 */
import type { components } from './schema'

type S = components['schemas']

export type Role = S['Role']
export type SessionUser = S['SessionUser']
export type LoginResult = S['LoginResult']
export type Policy = S['Policy']
export type Me = S['Me']
export type SetupStatus = S['SetupStatus']
export type SetupRequest = S['SetupRequest']
export type Passkey = S['Passkey']
export type SecuritySummary = S['SecuritySummary']
export type TotpEnrollment = S['TotpEnrollment']
export type MfaEnrolled = S['MfaEnrolled']
export type SessionInfo = S['SessionInfo']
export type Invitation = S['Invitation']
export type InviteCreated = S['InviteCreated']
export type AdminUser = S['AdminUser']
export type AdminUserCreate = S['AdminUserCreate']
export type AdminUserCreated = S['AdminUserCreated']
export type SecuritySettings = S['SecuritySettings']
export type SecuritySettingsPatch = S['SecuritySettingsPatch']
/** 标准 WebAuthn JSON（选项或响应），原样透传给 @simplewebauthn/browser / 后端。 */
export type WebAuthnJSON = S['WebAuthnJSON']
export interface WebAuthnBegin { challenge_id: string; options: WebAuthnJSON }
export interface AdminUserQuery { page?: number; page_size?: number; q?: string; role?: Role; status?: 'active' | 'disabled' | 'locked' }
export type AdminUserPatch = { display_name?: string; role?: Role; status?: 'active' | 'disabled' }

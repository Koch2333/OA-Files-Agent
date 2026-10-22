import type { LlmConfig } from '../api/types'

/** 向导“配置全局大模型”一步的逻辑。 */
export type LlmStepState = 'configured' | 'pending'

export function llmStepState(saved: Pick<LlmConfig, 'mode' | 'state'> | null | undefined): LlmStepState {
  return saved && saved.mode === 'llm' && saved.state === 'ok' ? 'configured' : 'pending'
}

export const LLM_SKIP_HINT = '稍后在 设置 中配置；未配置时 AI 工作台不可用'

/** 完成页摘要。 */
export function llmSummary(saved: Pick<LlmConfig, 'mode' | 'state' | 'model'> | null | undefined, allowPersonal: boolean): { llm: string; personal: string } {
  return {
    llm: llmStepState(saved) === 'configured' ? `已配置（${saved!.model}）` : '未配置（AI 工作台暂不可用，可稍后在 设置 中配置）',
    personal: allowPersonal ? '允许员工配置个人大模型 API' : '不允许（默认）',
  }
}

/** 进入下一步时需要写入的策略：只有开启时才调用接口（默认值即关闭）。 */
export function policyPatchForWizard(allowPersonal: boolean): { allow_personal_llm: true } | null {
  return allowPersonal ? { allow_personal_llm: true } : null
}

/**
 * 大模型一步的按钮：未配置时“继续”不可用，必须显式点击“跳过”（跳过不是隐式的）；
 * 读取/保存出错时本步保持打开，提供“重试”与“跳过”，绝不自动进入下一步。
 */
export function llmStepActions(state: LlmStepState, error: string | null): { canContinue: boolean; canSkip: boolean; showRetry: boolean } {
  return { canContinue: state === 'configured' && !error, canSkip: state !== 'configured' || !!error, showRetry: !!error }
}

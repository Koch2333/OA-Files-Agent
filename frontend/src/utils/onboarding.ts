import type { Role } from '../api'

/** 新手引导：首次登录自动出现一次，可随时跳过，也可从用户菜单重新打开。步骤数据按角色筛选（只读账号看不到导入/审批步骤）。 */
export interface TourStep { id: string; icon: string; title: string; body: string[]; to?: string; toLabel?: string; needsWrite?: boolean }

export const TOUR_KEY = 'oa.onboarding.v1'

const STEPS: TourStep[] = [
  { id: 'import', icon: 'cloud_upload', title: '第 1 步：把文件拖进来', needsWrite: true, to: '/', toLabel: '去工作台',
    body: ['在工作台把文件、文件夹或压缩包（ZIP / 7z / RAR）拖进页面（也可以直接粘贴）。上传没结束时还能继续添加，它们会并入同一个任务。', '这一步只读取你选的文件，不会移动或修改任何原文件。'] },
  { id: 'recognize', icon: 'auto_awesome', title: '第 2 步：AI 识别并生成计划', needsWrite: true,
    body: ['系统会识别每个文件的部门、类型和日期，并生成「改什么名、放到哪个目录」的归档计划。', '文件里本来没有日期是正常的，不会被当作问题。拿不准的部门或类型会先标成「其他」，系统不会替你编造。'] },
  { id: 'review', icon: 'fact_check', title: '第 3 步：审核计划', needsWrite: true, to: '/review-queue', toLabel: '看待审核队列',
    body: ['在计划页可以直接修订某一项；暂时不想处理的勾选「稍后审核」，它们会进入待审核队列，其余的先批准；识别得不对的勾选「退回 AI 重新识别」，只有这几个文件会再次调用模型。', '批准后点「执行归档」，文件才会真正复制到归档目录；原件不会被覆盖。'] },
  { id: 'auto', icon: 'bolt', title: '全自动模式（可选）', needsWrite: true,
    body: ['开始归档前可以选「全自动」：模型直接决定并归档，不需要逐个审核。拿不准的字段用「其他」「未知日期」占位，归档后再去台账筛选修改。', '读不出内容的文件和与已归档文件重复的文件，仍然会留给你确认。第一次使用需要先确认风险。'] },
  { id: 'ledger', icon: 'help', title: '台账与文件管理', to: '/ledger', toLabel: '打开台账',
    body: ['每次归档都会记入台账：可以搜索、筛选、导出 Excel。点一条记录，可以「修订分类」，文件会自动移到新的目录并重命名。', '「文件管理」里可以浏览归档目录、移动、重命名、下载，台账会同步更新。'] },
]

export function tourSteps(role: Role | null | undefined, canWrite: boolean): TourStep[] {
  if (!role) return []
  return STEPS.filter((s) => !s.needsWrite || canWrite)
}

/** 是否已看过（本浏览器）。存储不可用时视为已看过，避免每次都弹。 */
export function tourSeen(): boolean { try { return localStorage.getItem(TOUR_KEY) === '1' } catch { return true } }
export function markTourSeen(): void { try { localStorage.setItem(TOUR_KEY, '1') } catch { /* 存储不可用：页面照常工作 */ } }

/** 是否应该自动弹出：没看过，且浏览器不是被自动化脚本（端到端测试）控制的；菜单里手动打开不受影响。 */
export function tourAutoShow(): boolean {
  let automated = false
  try { automated = typeof navigator !== 'undefined' && navigator.webdriver === true } catch { automated = false }
  return !automated && !tourSeen()
}

'use strict'
// Turns a startup failure (exit code + backend log tail) into a short Chinese hint.
function diagnose(text) {
  const t = String(text || '')
  const rules = [
    [/address already in use|only one usage of each socket|bind: |EADDRINUSE/i, '端口被占用', '本机端口被其他程序占用。请关闭占用该端口的程序后点击“重试”（重试会自动换一个端口）。'],
    [/virus|病毒|operation did not complete successfully because the file|EPERM|EACCES|access is denied|拒绝访问|spawn UNKNOWN|ENOENT.*oa-agent/i, '程序被安全软件拦截', '后台程序可能被杀毒/安全软件拦截或隔离。请把安装目录与数据目录加入白名单，必要时重新安装，然后点击“重试”。'],
    [/worker executable not found|embedded PostgreSQL not found/i, '安装文件不完整', '程序文件缺失（可能被安全软件删除）。请重新安装。'],
    [/PG_VERSION|could not locate a valid checkpoint|invalid checkpoint|database system identifier|pg_ctl start failed|database password file missing|corrupt|损坏|PANIC/i, '数据库启动失败', '本地数据库可能因异常关机而损坏，或被另一个实例占用。请先彻底退出所有 OA 进程后重试；仍失败请查看日志，必要时备份并重命名数据目录中的 pg 文件夹。'],
    [/disk full|No space left|磁盘空间不足|ENOSPC/i, '磁盘空间不足', '数据目录所在磁盘空间不足，请释放空间后重试。'],
  ]
  for (const [re, title, hint] of rules) if (re.test(t)) return { title, hint }
  return { title: '服务启动失败', hint: '后台服务没有在预期时间内就绪。请点击“查看日志”了解详情，或重试。' }
}

module.exports = { diagnose }

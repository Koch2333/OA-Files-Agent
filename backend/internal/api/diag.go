package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"oa-agent/backend/internal/buildinfo"
	"oa-agent/backend/internal/db"
	"oa-agent/backend/internal/diag"
)

// DiagInfo is what the diagnostics routes need from the process (set by cmd/oa-agent; zero values are fine in tests).
type DiagInfo struct {
	StartedAt   time.Time
	Standalone  bool
	ArchiveRoot string
	// LogDir holds oa-agent.log, worker.log, postgres.log and crash-*.txt (data/logs); empty when the process only
	// logs to stderr.
	LogDir string
	// Config returns the effective non-secret process configuration for the diagnostic bundle (listen address,
	// directories, flags). It is redacted again before it is written.
	Config func() map[string]any
}

const (
	clientErrorBodyLimit = 8 << 10
	bundleMaxBytes       = 20 << 20
	bundleLogTail        = 512 << 10
	bundleOtherLogTail   = 256 << 10
	bundleReports        = 200
)

func (s *Server) diagRoutes() {
	s.handle("/v1/admin/system", map[string]http.HandlerFunc{"GET": s.diagSystem})
	s.handle("/v1/admin/error-reports", map[string]http.HandlerFunc{"GET": s.diagList})
	s.handle("/v1/admin/error-reports/{id}", map[string]http.HandlerFunc{"GET": s.diagGet, "PATCH": s.diagPatch, "DELETE": s.diagDelete})
	s.handle("/v1/admin/diagnostics/bundle", map[string]http.HandlerFunc{"GET": s.diagBundle})
	s.handle("/v1/client-errors", map[string]http.HandlerFunc{"POST": s.clientError})
}

func depState(st string) map[string]any {
	switch st {
	case "ok":
		return map[string]any{"ok": true, "message": "正常"}
	case "not_configured":
		return map[string]any{"ok": false, "message": "未配置"}
	case "timeout":
		return map[string]any{"ok": false, "message": "检测超时"}
	default:
		return map[string]any{"ok": false, "message": "不可用：" + diag.Truncate(diag.Redact(st), 120)}
	}
}

func (s *Server) systemInfo(ctx context.Context) map[string]any {
	dbs, ws := s.dependencyStates(ctx)
	started := s.d.Diag.StartedAt
	if started.IsZero() {
		started = processStart
	}
	disk := map[string]any{"archive_path": s.d.Diag.ArchiveRoot, "free_bytes": 0, "total_bytes": 0}
	if s.d.Diag.ArchiveRoot != "" {
		if free, total, err := diag.DiskUsage(s.d.Diag.ArchiveRoot); err == nil {
			disk["free_bytes"], disk["total_bytes"] = free, total
		}
	}
	unclean, open := false, 0
	if s.d.Store != nil && s.d.Store.Pool != nil && dbs == "ok" {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if v, err := diag.UncleanExitOpen(cctx, s.d.Store.Pool); err == nil {
			unclean = v
		}
		if n, err := diag.OpenCount(cctx, s.d.Store.Pool); err == nil {
			open = n
		}
	}
	return map[string]any{
		"version": buildinfo.Version, "commit": buildinfo.Commit, "build_mode": buildinfo.Mode(), "go_version": runtime.Version(),
		"os": runtime.GOOS + "/" + runtime.GOARCH, "standalone": s.d.Diag.Standalone, "started_at": started.UTC().Format(time.RFC3339),
		"uptime_seconds": int64(time.Since(started).Seconds()), "database": depState(dbs), "worker": depState(ws), "disk": disk,
		"last_exit_unclean": unclean, "open_error_reports": open,
	}
}

var processStart = time.Now()

func (s *Server) diagSystem(w http.ResponseWriter, r *http.Request) {
	s.ok(w, 200, s.systemInfo(r.Context()))
}

func (s *Server) diagList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := diag.Filter{Source: q.Get("source"), Severity: q.Get("severity"), Q: strings.TrimSpace(q.Get("q"))}
	if f.Source != "" && !map[string]bool{"api": true, "pipeline": true, "worker": true, "storage": true, "client": true, "supervisor": true, "startup": true, "llm": true}[f.Source] {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "source 取值无效", nil)
		return
	}
	if f.Severity != "" && f.Severity != "error" && f.Severity != "panic" && f.Severity != "fatal" {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "severity 取值无效", nil)
		return
	}
	if v := q.Get("resolved"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil || (v != "true" && v != "false") {
			s.fail(w, r, 400, "INVALID_ARGUMENT", "resolved 取值无效", nil)
			return
		}
		f.Resolved = &b
	}
	switch st := q.Get("state"); st {
	case "":
	case "all":
		f.Resolved = nil
	case "open", "resolved":
		b := st == "resolved"
		f.Resolved = &b
	default:
		s.fail(w, r, 400, "INVALID_ARGUMENT", "state 取值无效", nil)
		return
	}
	if len([]rune(f.Q)) > 200 {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "q 过长", nil)
		return
	}
	f.Page, f.Size = pageParams(r)
	items, total, err := diag.List(r.Context(), s.d.Store.Pool, f)
	if err != nil {
		s.internal(w, r, "内部错误", err)
		return
	}
	s.list(w, items, total, f.Page, f.Size)
}

func (s *Server) diagErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, diag.ErrNotFound) {
		s.fail(w, r, 404, "NOT_FOUND", "错误报告不存在", nil)
		return
	}
	s.internal(w, r, "内部错误", err)
}

func (s *Server) diagGet(w http.ResponseWriter, r *http.Request) {
	d, err := diag.Get(r.Context(), s.d.Store.Pool, r.PathValue("id"))
	if err != nil {
		s.diagErr(w, r, err)
		return
	}
	s.ok(w, 200, d)
}

func (s *Server) diagPatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Resolved *bool `json:"resolved"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Resolved == nil {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "缺少 resolved", nil)
		return
	}
	actor := actorOf(r)
	d, err := diag.SetResolved(r.Context(), s.d.Store.Pool, r.PathValue("id"), *in.Resolved, actor)
	if err != nil {
		s.diagErr(w, r, err)
		return
	}
	act := "diagnostics.resolve"
	if !*in.Resolved {
		act = "diagnostics.reopen"
	}
	s.d.Store.Audit(r.Context(), actor, act, d.ID, "错误报告 "+diag.ShortID(d.ID)+"："+d.Code)
	s.ok(w, 200, d)
}

func (s *Server) diagDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := diag.Delete(r.Context(), s.d.Store.Pool, id); err != nil {
		s.diagErr(w, r, err)
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "diagnostics.delete", id, "删除错误报告 "+diag.ShortID(id))
	w.WriteHeader(http.StatusNoContent)
}

// --- client errors

func (s *Server) clientErrorLimiter() *diag.Limiter {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	if s.clientLim == nil {
		s.clientLim = diag.NewLimiter(20, time.Minute)
	}
	return s.clientLim
}

func (s *Server) clientError(w http.ResponseWriter, r *http.Request) {
	key := actorOf(r)
	if id, ok := identityOf(r); ok && id.User.ID != "" {
		key = id.User.ID
	}
	if ok, wait := s.clientErrorLimiter().Allow(key); !ok {
		secs := int(wait/time.Second) + 1
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		s.fail(w, r, 429, "RATE_LIMITED", "上报过于频繁，请稍后再试", map[string]any{"retry_after_seconds": secs})
		return
	}
	var in struct {
		Message    string `json:"message"`
		Stack      string `json:"stack"`
		Route      string `json:"route"`
		Component  string `json:"component"`
		AppVersion string `json:"app_version"`
		RequestID  string `json:"request_id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, clientErrorBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			s.fail(w, r, 400, "BODY_TOO_LARGE", "请求体超过 8 KiB 上限", nil)
		} else {
			s.fail(w, r, 400, "INVALID_JSON", "请求体不是有效的 JSON 或含未知字段", nil)
		}
		return
	}
	if strings.TrimSpace(in.Message) == "" {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "message 不能为空", nil)
		return
	}
	user := ""
	if id, ok := identityOf(r); ok {
		user = id.User.Username
	}
	rid := in.RequestID
	if strings.TrimSpace(rid) == "" {
		rid = requestID(r)
	}
	id := diag.Report(r.Context(), diag.Entry{
		Source: "client", Code: "CLIENT_ERROR", Message: diag.Truncate(in.Message, 500), Stack: diag.Truncate(in.Stack, 4000),
		RequestID: diag.Truncate(rid, 64), User: user,
		Details: map[string]any{"route": diag.Truncate(in.Route, 200), "component": diag.Truncate(in.Component, 120), "app_version": diag.Truncate(in.AppVersion, 40)},
	})
	if id == "" {
		s.fail(w, r, 500, "INTERNAL", "内部错误", nil)
		return
	}
	s.ok(w, 202, map[string]any{"report_id": id})
}

// --- diagnostic bundle

// budgetZip adds files to a streaming zip until a total size budget is used up.
type budgetZip struct {
	zw      *zip.Writer
	left    int64
	skipped []string
	now     time.Time
}

func (b *budgetZip) add(name string, data []byte) {
	if int64(len(data)) > b.left {
		b.skipped = append(b.skipped, name)
		return
	}
	fw, err := b.zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: b.now})
	if err != nil {
		b.skipped = append(b.skipped, name)
		return
	}
	n, _ := fw.Write(data)
	b.left -= int64(n)
}

func (b *budgetZip) addJSON(name string, v any) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		raw = []byte(fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	b.add(name, raw)
}

// tailRedacted returns the last n bytes of a text file (from a line boundary), redacted.
func tailRedacted(path string, n int64) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false
	}
	off, cut := int64(0), false
	if fi.Size() > n {
		off, cut = fi.Size()-n, true
	}
	buf := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return nil, false
	}
	if cut {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return []byte(diag.Redact(strings.ToValidUTF8(string(buf), "?"))), true
}

const bundleReadme = `OA Agent 诊断包 / diagnostic bundle

包含（均已脱敏）：
  VERSION.txt            版本、提交、构建模式、Go 运行时、操作系统、启动时间、内存与协程统计
  environment.txt        OA_* 环境变量名；只有少数非敏感项显示取值，其余一律显示 [set]
  config.json            生效的非密钥配置：监听地址、目录、模式、策略开关、LLM 模式/模型/服务主机、存储连接种类
  system.json            数据库/Worker 状态、磁盘、上次是否异常退出、未处理错误数
  migrations.json        数据库迁移版本与本程序期望版本
  logs/oa-agent.log      最近的后端日志（经脱敏）；logs/worker.log、logs/postgres.log 同理（存在时）
  crash/crash-*.txt      最近的崩溃文件（写入时已脱敏，打包时再脱敏一次）
  error-reports.json     最近的错误报告（按指纹聚合，含调用栈、次数、最近 20 次发生）

不包含：
  - API Key、令牌、密码、Cookie、会话、恢复码、TOTP 密钥、WebAuthn 凭据、存储连接凭据
  - 文档正文、文件内容、OCR 文本、提示词与模型回复
  - 数据库内容（台账、任务、用户表等）与归档文件本身
  - 邮箱地址与用户主目录中的用户名（日志与报告里会替换为 [EMAIL] / [USER]）

脱敏是尽力而为的模式匹配；发送前请快速浏览一遍。若发现疏漏，请告知开发者补充规则。
`

func (s *Server) bundleConfig(ctx context.Context) map[string]any {
	cfg := map[string]any{}
	if s.d.Diag.Config != nil {
		for k, v := range s.d.Diag.Config() {
			cfg[k] = v
		}
	}
	cfg["standalone"] = s.d.Diag.Standalone
	cfg["strict_local"] = s.d.StrictLocal
	cfg["trusted_proxy"] = s.d.TrustedProxy
	cfg["cors_origins"] = s.d.CORSOrigins
	cfg["frontend_served"] = s.d.StaticDir != ""
	cfg["archive_root"] = s.d.Diag.ArchiveRoot
	if s.d.LLM != nil {
		c := s.d.LLM.Config()
		host := ""
		if i := strings.Index(c.BaseURL, "://"); i >= 0 {
			host = c.BaseURL[i+3:]
		} else {
			host = c.BaseURL
		}
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
		if i := strings.LastIndexByte(host, '@'); i >= 0 {
			host = host[i+1:]
		}
		cfg["llm"] = map[string]any{"mode": c.Mode, "preset": c.PresetID, "model": c.Model, "vision_model": c.VisionModel, "allow_vision": c.AllowVision, "base_host": host, "api_key_set": c.APIKeySet, "state": c.State}
	}
	if s.d.Auth != nil {
		if set, err := s.d.Auth.Settings(ctx); err == nil {
			cfg["auth"] = map[string]any{"mode": set.Mode, "setup_completed": set.SetupCompleted, "mfa_policy": set.MFAPolicy, "allow_password_login": set.AllowPasswordLogin,
				"session_idle_minutes": set.SessionIdleMinutes, "session_max_hours": set.SessionMaxHours, "lockout_threshold": set.LockoutThreshold, "lockout_minutes": set.LockoutMinutes}
		}
	}
	if s.d.Store != nil && s.d.Store.Pool != nil {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if rows, err := s.d.Store.Pool.Query(cctx, `SELECT kind, count(*), count(*) FILTER (WHERE enabled), count(*) FILTER (WHERE replicate <> 'off') FROM storage_connections GROUP BY kind ORDER BY kind`); err == nil {
			var list []map[string]any
			for rows.Next() {
				var kind string
				var n, en, rep int
				if rows.Scan(&kind, &n, &en, &rep) == nil {
					list = append(list, map[string]any{"kind": kind, "connections": n, "enabled": en, "replicating": rep})
				}
			}
			rows.Close()
			cfg["storage_connections"] = list
		}
	}
	return diag.RedactValue(toAny(cfg)).(map[string]any)
}

func (s *Server) migrationInfo(ctx context.Context) map[string]any {
	out := map[string]any{"expected_version": db.SchemaVersion}
	if s.d.Store == nil || s.d.Store.Pool == nil {
		return out
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := s.d.Store.Pool.Query(cctx, `SELECT version, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		out["error"] = diag.Redact(err.Error())
		return out
	}
	defer rows.Close()
	var applied []map[string]any
	maxV := 0
	for rows.Next() {
		var v int
		var at time.Time
		if rows.Scan(&v, &at) == nil {
			applied = append(applied, map[string]any{"version": v, "applied_at": at.UTC().Format(time.RFC3339)})
			if v > maxV {
				maxV = v
			}
		}
	}
	out["current_version"], out["applied"] = maxV, applied
	return out
}

func (s *Server) diagBundle(w http.ResponseWriter, r *http.Request) {
	actor := actorOf(r)
	s.d.Store.Audit(r.Context(), actor, "diagnostics.bundle", "bundle", "下载诊断包")
	now := time.Now().UTC()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="oa-agent-diagnostics-`+now.Format("20060102-150405")+`.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	zw := zip.NewWriter(w)
	b := &budgetZip{zw: zw, left: bundleMaxBytes - 64<<10, now: now}
	ctx := r.Context()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	ver := fmt.Sprintf("version: %s\ncommit: %s\nbuild_mode: %s\nbuild: %s\ngo: %s\nos_arch: %s/%s\ncpus: %d\ngoroutines: %d\nheap_alloc_bytes: %d\nheap_sys_bytes: %d\ngc_cycles: %d\nstarted_at: %s\ngenerated_at: %s\nstandalone: %v\n",
		buildinfo.Version, buildinfo.Commit, buildinfo.Mode(), buildinfo.String(), runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.NumGoroutine(),
		ms.HeapAlloc, ms.HeapSys, ms.NumGC, orTime(s.d.Diag.StartedAt, processStart).Format(time.RFC3339), now.Format(time.RFC3339), s.d.Diag.Standalone)
	b.add("VERSION.txt", []byte(ver))

	var env strings.Builder
	for _, e := range diag.EnvSummary() {
		env.WriteString(e.Name + "=" + e.Value + "\n")
	}
	b.add("environment.txt", []byte(env.String()))
	b.addJSON("config.json", s.bundleConfig(ctx))
	b.addJSON("system.json", diag.RedactValue(toAny(s.systemInfo(ctx))))
	b.addJSON("migrations.json", s.migrationInfo(ctx))

	if dir := s.d.Diag.LogDir; dir != "" {
		for _, l := range []struct {
			name string
			n    int64
		}{{"oa-agent.log", bundleLogTail}, {"worker.log", bundleOtherLogTail}, {"postgres.log", bundleOtherLogTail}} {
			if data, ok := tailRedacted(filepath.Join(dir, l.name), l.n); ok {
				b.add("logs/"+l.name, data)
			}
		}
		crashes, _ := filepath.Glob(filepath.Join(dir, "crash-*.txt"))
		sort.Sort(sort.Reverse(sort.StringSlice(crashes)))
		for i, p := range crashes {
			if i >= 5 {
				break
			}
			if data, ok := tailRedacted(p, 64<<10); ok {
				b.add("crash/"+filepath.Base(p), data)
			}
		}
	}
	if s.d.Store != nil && s.d.Store.Pool != nil {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		reports, err := diag.Recent(cctx, s.d.Store.Pool, bundleReports)
		cancel()
		if err != nil {
			b.addJSON("error-reports.json", map[string]any{"error": diag.Redact(err.Error())})
		} else {
			b.addJSON("error-reports.json", diag.RedactValue(toAny(reports)))
		}
	}
	readme := bundleReadme
	if len(b.skipped) > 0 {
		readme += "\n以下文件因超过 20 MiB 总大小上限被省略：" + strings.Join(b.skipped, ", ") + "\n"
	}
	b.left += 64 << 10
	b.add("README.txt", []byte(readme))
	_ = zw.Close()
}

func orTime(t, d time.Time) time.Time {
	if t.IsZero() {
		return d
	}
	return t
}

// toAny round-trips a value through JSON so RedactValue can walk it.
func toAny(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

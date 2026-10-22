package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/xuri/excelize/v2"

	"oa-agent/backend/internal/store"
)

const maxExportRows = 100000

var excelHeaders = []string{"序号", "原文件名", "规范文件名", "所属部门", "文件类型", "生成日期", "归档路径", "SHA-256", "任务 ID", "归档时间", "决策方式"}

// decisionLabel is the "决策方式" column: who decided the archive location.
func decisionLabel(e store.LedgerEntry) string {
	if e.Decision == store.DecisionAIAuto {
		if e.DecisionModel != "" {
			return "全自动（模型：" + e.DecisionModel + "）"
		}
		return "全自动"
	}
	return "人工审核"
}

func (s *Server) exportLedger(w http.ResponseWriter, r *http.Request) {
	if !validDecision(r) {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "decision 须为 manual 或 ai_auto", nil)
		return
	}
	rows, err := s.d.Store.LedgerAll(r.Context(), ledgerFilter(r), maxExportRows)
	if err != nil {
		s.storeErr(w, r, err, "台账")
		return
	}
	f := excelize.NewFile()
	defer f.Close()
	const sheet = "归档台账"
	_ = f.SetSheetName("Sheet1", sheet)
	sw, err := f.NewStreamWriter(sheet)
	if err != nil {
		s.fail(w, r, 500, "INTERNAL", "导出失败", nil)
		return
	}
	hdr, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Color: []string{"#E8DEF8"}, Pattern: 1}})
	for i, w := range []float64{6, 34, 48, 12, 12, 12, 64, 66, 38, 20, 30} {
		_ = sw.SetColWidth(i+1, i+1, w)
	}
	_ = sw.SetPanes(&excelize.Panes{Freeze: true, Split: false, XSplit: 0, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	hrow := make([]any, len(excelHeaders))
	for i, h := range excelHeaders {
		hrow[i] = excelize.Cell{Value: h, StyleID: hdr}
	}
	_ = sw.SetRow("A1", hrow)
	loc := time.Local
	for i, e := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		_ = sw.SetRow(cell, []any{i + 1, e.OriginalName, e.CanonicalName, e.Department, e.DocumentType, e.DocumentDate, e.ArchivePath, e.SHA256, e.TaskID, e.ExecutedAt.In(loc).Format("2006-01-02 15:04:05"), decisionLabel(e)})
	}
	if err = sw.Flush(); err != nil {
		s.fail(w, r, 500, "INTERNAL", "导出失败", nil)
		return
	}
	_ = f.AutoFilter(sheet, fmt.Sprintf("A1:K%d", len(rows)+1), nil)
	var buf bytes.Buffer
	if err = f.Write(&buf); err != nil {
		s.fail(w, r, 500, "INTERNAL", "导出失败", nil)
		return
	}
	name := "归档台账_" + time.Now().Format("20060102_150405") + ".xlsx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=\"ledger.xlsx\"; filename*=UTF-8''"+url.PathEscape(name))
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
	s.d.Store.Audit(r.Context(), actorOf(r), "ledger.export", "ledger", fmt.Sprintf("导出台账 %d 行", len(rows)))
}

// taskEvents streams persisted events as SSE. Event ids are per-task monotonic sequence numbers; clients
// resume with Last-Event-ID. Only redacted summaries are ever stored, never document text or prompts.
func (s *Server) taskEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.d.Store.GetJob(r.Context(), id); err != nil {
		s.storeErr(w, r, err, "任务")
		return
	}
	var last int64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			s.fail(w, r, 400, "INVALID_LAST_EVENT_ID", "Last-Event-ID 必须是非负整数", nil)
			return
		}
		last = n
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, r, 500, "INTERNAL", "不支持流式响应", nil)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	_, _ = fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	poll := time.NewTicker(500 * time.Millisecond)
	beat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer beat.Stop()
	for {
		evs, err := s.d.Store.Events(r.Context(), id, last, 200)
		if err != nil {
			return
		}
		for _, e := range evs {
			b, _ := json.Marshal(e)
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.EventType, b); err != nil {
				return
			}
			last = e.ID
		}
		if len(evs) > 0 {
			fl.Flush()
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			fl.Flush()
		case <-poll.C:
		}
	}
}

var _ = store.ErrNotFound

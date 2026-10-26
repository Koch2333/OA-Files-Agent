package api

import (
	"net/http"

	"oa-agent/backend/internal/archive"
	"oa-agent/backend/internal/harness"
)

// classificationOptions suggests values for the manual department/type fields: built-in vocabulary, values
// already used in the ledger (most used first) and, with task_id, values in that task's plans. 其他 is last.
func (s *Server) classificationOptions(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID != "" && !isUUID(taskID) {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "task_id 无效", nil)
		return
	}
	names := func(v []harness.VocabEntry) []string {
		out := make([]string, len(v))
		for i, e := range v {
			out[i] = e.Name
		}
		return out
	}
	build := func(col string, builtin []harness.VocabEntry) ([]archive.CategoryOption, bool) {
		ledger, plan, err := s.d.Store.ClassificationUsage(r.Context(), col, taskID)
		if err != nil {
			s.storeErr(w, r, err, "分类选项")
			return nil, false
		}
		return archive.MergeCategoryOptions(names(builtin), ledger, plan, archive.MaxCategoryOptions), true
	}
	deps, ok := build("department", harness.DefaultDepartments())
	if !ok {
		return
	}
	types, ok := build("document_type", harness.DefaultDocumentTypes())
	if !ok {
		return
	}
	s.ok(w, 200, map[string]any{"departments": deps, "types": types, "reserved": archive.CategoryOther})
}

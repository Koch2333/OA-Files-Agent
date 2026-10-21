package harness

// DefaultDepartments is the built-in department vocabulary.
func DefaultDepartments() []VocabEntry {
	return []VocabEntry{
		{Name: "财务部", Keywords: []string{"财务部", "财务", "会计", "报销", "出纳", "预算", "付款"}},
		{Name: "人事部", Keywords: []string{"人事部", "人力资源", "人力", "人事", "招聘", "薪酬", "考勤", "入职", "离职"}},
		{Name: "采购部", Keywords: []string{"采购部", "采购", "供应商", "询价", "招标"}},
		{Name: "行政部", Keywords: []string{"行政部", "行政", "办公室", "后勤"}},
	}
}

// DefaultDocumentTypes is the built-in document type vocabulary.
func DefaultDocumentTypes() []VocabEntry {
	return []VocabEntry{
		{Name: "申请", Keywords: []string{"申请", "报销", "借款", "请假", "申领"}},
		{Name: "通知", Keywords: []string{"通知"}},
		{Name: "公告", Keywords: []string{"公告"}},
		{Name: "合同", Keywords: []string{"合同", "协议"}},
		{Name: "报告", Keywords: []string{"报告", "汇报", "总结"}},
		{Name: "发票", Keywords: []string{"发票"}},
		{Name: "报表", Keywords: []string{"报表", "明细表", "统计表", "汇总表"}},
		{Name: "会议纪要", Keywords: []string{"会议纪要", "纪要"}},
		{Name: "制度", Keywords: []string{"制度", "管理办法", "规定", "细则"}},
		{Name: "方案", Keywords: []string{"方案", "计划"}},
		{Name: "请示", Keywords: []string{"请示"}},
		{Name: "批复", Keywords: []string{"批复"}},
		{Name: "清单", Keywords: []string{"清单"}},
	}
}

// resolveVocab maps free text to a canonical vocabulary name when the value
// equals the name or contains one of its keywords.
func resolveVocab(v []VocabEntry, value string) (string, bool) {
	for _, e := range v {
		if value == e.Name {
			return e.Name, true
		}
	}
	for _, e := range v {
		for _, k := range append([]string{e.Name}, e.Keywords...) {
			if k != "" && containsStr(value, k) {
				return e.Name, true
			}
		}
	}
	return "", false
}

func inVocab(v []VocabEntry, name string) bool {
	for _, e := range v {
		if e.Name == name {
			return true
		}
	}
	return false
}

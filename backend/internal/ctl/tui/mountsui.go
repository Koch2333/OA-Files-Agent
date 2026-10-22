package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"oa-agent/backend/internal/ctl/mounts"
	"oa-agent/backend/internal/ctl/ops"
)

const (
	mList = iota
	mForm
	mReview
	mRun
	mRemove
)

type mountsScreen struct {
	b       *Backend
	cfg     mounts.Config
	fromFil bool
	rows    []mounts.CheckResult
	cur     int
	st      int
	err     string

	// form
	kind    string // add | extra | core
	role    string
	name    textinput.Model
	path    textinput.Model
	ro      bool
	focus   int
	cands   []string
	liveMsg []mounts.Issue
	next    mounts.Config
	hasNext bool

	// review
	as  ops.Assessment
	ack textinput.Model
	run runView
}

func newMountsScreen(b *Backend) *mountsScreen {
	s := &mountsScreen{b: b, name: newInput(), path: newInput(), ack: newInput()}
	s.name.CharLimit = 32
	s.path.CharLimit = 512
	s.reload()
	return s
}

func (s *mountsScreen) Title() string { return sMounts }
func (s *mountsScreen) Init() tea.Cmd { return nil }
func (s *mountsScreen) Close()        { s.run.cancel() }

func (s *mountsScreen) Help() string {
	switch s.st {
	case mForm:
		return sHelpForm + "  ↑/↓ 切换字段"
	case mReview:
		return sReviewHelp
	case mRun:
		return sHelpRun
	case mRemove:
		return sYNConfirm
	}
	return sMountsHelp
}

func (s *mountsScreen) reload() {
	cfg, ff, err := s.b.Mounts()
	if err != nil {
		s.err = err.Error()
		return
	}
	s.err = ""
	s.cfg, s.fromFil = cfg, ff
	s.rows = mounts.CheckAll(cfg, s.b.S.Prober)
	if s.cur >= len(s.rows) {
		s.cur = 0
	}
}

func (s *mountsScreen) opt() mounts.Options { return mounts.Options{InstallDir: s.b.Dir()} }

// compute builds the proposed config from the form and refreshes the live messages.
func (s *mountsScreen) compute() {
	s.hasNext = false
	var issues []mounts.Issue
	var next mounts.Config
	switch s.kind {
	case "add":
		nm := strings.TrimSpace(s.name.Value())
		if nm == "" || strings.TrimSpace(s.path.Value()) == "" {
			s.liveMsg = nil
			return
		}
		next, issues = mounts.AddExtra(s.cfg, nm, strings.TrimSpace(s.path.Value()), s.ro, false, s.opt())
	case "extra":
		next, issues = mounts.AddExtra(s.cfg, s.name.Value(), strings.TrimSpace(s.path.Value()), s.ro, true, s.opt())
	case "core":
		var err error
		next, issues, err = mounts.SetCore(s.cfg, s.role, strings.TrimSpace(s.path.Value()), s.opt())
		if err != nil {
			issues = append(issues, mounts.Issue{Sev: mounts.Error, Text: err.Error()})
		}
	}
	if !mounts.HasErrors(issues) {
		as := s.b.Assess(context.Background(), s.cfg, next)
		issues = as.Issues
	}
	s.liveMsg = issues
	if !mounts.HasErrors(issues) {
		s.next, s.hasNext = next, true
	}
}

func (s *mountsScreen) openForm(kind, role string) {
	s.kind, s.role, s.st, s.focus, s.cands = kind, role, mForm, 0, nil
	s.name.SetValue("")
	s.path.SetValue("")
	s.ro = true
	s.liveMsg = nil
	switch kind {
	case "add":
		s.name.Focus()
		s.path.Blur()
	case "extra":
		x := s.cfg.Extra[s.cfg.FindExtra(role)]
		s.name.SetValue(x.Name)
		s.path.SetValue(x.Host)
		s.ro = x.ReadOnly
		s.focus = 1
		s.name.Blur()
		s.path.Focus()
	case "core":
		s.path.SetValue(s.cfg.CoreHost(role))
		s.focus = 1
		s.path.Focus()
	}
	s.compute()
}

func (s *mountsScreen) setFocus(f int) {
	if s.kind != "add" && f == 0 {
		f = 1
	}
	if f < 0 {
		f = 0
	}
	if f > 2 || (s.kind == "core" && f > 1) {
		f = 1
		if s.kind == "add" || s.kind == "extra" {
			f = 2
		}
	}
	s.focus = f
	s.name.Blur()
	s.path.Blur()
	switch f {
	case 0:
		s.name.Focus()
	case 1:
		s.path.Focus()
	}
}

func (s *mountsScreen) toReview(next mounts.Config) {
	s.next = next
	s.as = s.b.Assess(context.Background(), s.cfg, next)
	if s.as.MissingDirs && s.b.S.Env.Getuid != nil && s.b.S.Env.Getuid() != 0 {
		s.as.Block = true
		for _, f := range s.as.Fixes {
			s.as.Issues = append(s.as.Issues, mounts.Issue{Sev: mounts.Error, Text: fmt.Sprintf(sMissingDirHint, f.Command())})
		}
	}
	s.ack.SetValue("")
	s.ack.Focus()
	s.st = mReview
}

func (s *mountsScreen) startApply(restart bool) tea.Cmd {
	s.st = mRun
	prev, next, fixes := s.cfg, s.next, s.as.Fixes
	return s.run.start(s.b.Ctx, "应用挂载变更", func(ctx context.Context, emit func(string)) error {
		err := s.b.ApplyMounts(ctx, prev, next, restart, fixes, emit)
		return err
	})
}

func (s *mountsScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	if ok, cmd := s.run.update(msg); ok {
		if s.run.done {
			if s.run.err != nil && strings.Contains(s.run.err.Error(), "回滚") {
				s.run.note = sRolledBack
			} else if s.run.err == nil {
				s.run.note = sApplied
			}
		}
		return s, cmd
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return s, nil
	}
	key := k.String()
	switch s.st {
	case mRun:
		if key == "esc" || s.run.done {
			s.run.cancel()
			s.st = mList
			s.reload()
		}
	case mRemove:
		switch key {
		case "y", "Y":
			n, err := mounts.RemoveExtra(s.cfg, s.name.Value())
			if err != nil {
				s.err = err.Error()
				s.st = mList
				return s, nil
			}
			s.toReview(n)
		case "n", "N", "esc":
			s.st = mList
		}
	case mReview:
		switch key {
		case "esc":
			s.st = mList
		case "enter", "y", "w":
			if s.as.Block {
				return s, nil
			}
			if s.as.NeedAck && strings.TrimSpace(s.ack.Value()) != "I-UNDERSTAND" {
				if key == "enter" || key == "y" || key == "w" {
					var c tea.Cmd
					if key != "enter" {
						s.ack, c = s.ack.Update(k)
					}
					return s, c
				}
			}
			return s, s.startApply(key != "w")
		default:
			if s.as.NeedAck {
				var c tea.Cmd
				s.ack, c = s.ack.Update(k)
				return s, c
			}
		}
	case mForm:
		return s.formKey(k)
	default:
		return s.listKey(key)
	}
	return s, nil
}

func (s *mountsScreen) listKey(key string) (screen, tea.Cmd) {
	switch key {
	case "esc", "q":
		return s, back
	case "up", "k":
		if s.cur > 0 {
			s.cur--
		}
	case "down", "j":
		if s.cur < len(s.rows)-1 {
			s.cur++
		}
	case "r":
		s.reload()
	case "a":
		s.openForm("add", "")
	case "e":
		if len(s.rows) == 0 {
			return s, nil
		}
		e := s.rows[s.cur].Entry
		if e.Role == "extra" {
			s.openForm("extra", e.Name)
		} else {
			s.openForm("core", e.Role)
		}
	case "x":
		if len(s.rows) > 0 && s.rows[s.cur].Entry.Role == "extra" {
			s.name.SetValue(s.rows[s.cur].Entry.Name)
			s.st = mRemove
		}
	case "A":
		s.toReview(s.cfg)
	}
	return s, nil
}

func (s *mountsScreen) formKey(k tea.KeyMsg) (screen, tea.Cmd) {
	switch key := k.String(); key {
	case "esc":
		s.st = mList
		return s, nil
	case "up":
		s.setFocus(s.focus - 1)
		return s, nil
	case "down":
		s.setFocus(s.focus + 1)
		return s, nil
	case "tab":
		if s.focus == 1 {
			v, c := nextCompletion(s.path.Value(), s.cands)
			s.cands = c
			s.path.SetValue(v)
			s.path.CursorEnd()
			s.compute()
		} else {
			s.setFocus(s.focus + 1)
		}
		return s, nil
	case "enter":
		s.compute()
		if s.hasNext {
			s.toReview(s.next)
		}
		return s, nil
	case " ":
		if s.focus == 2 {
			s.ro = !s.ro
			s.compute()
			return s, nil
		}
	}
	var c tea.Cmd
	switch s.focus {
	case 0:
		s.name, c = s.name.Update(k)
	case 1:
		s.path, c = s.path.Update(k)
		s.cands = nil
	}
	s.compute()
	return s, c
}

func issueLines(is []mounts.Issue, w int) string {
	var b strings.Builder
	for _, i := range is {
		lv := map[mounts.Severity]string{mounts.Info: "info", mounts.Warn: "warn", mounts.Error: "fail"}[i.Sev]
		b.WriteString(icon(lv) + " " + wrap(i.Text, w-3) + "\n")
		if i.Fix != "" {
			b.WriteString(stySubtle.Render("    $ "+i.Fix) + "\n")
		}
	}
	return b.String()
}

// wrap hard-wraps by cell width, indenting continuation lines.
func wrap(s string, w int) string {
	if w < 10 || cells(s) <= w {
		return s
	}
	var out []string
	cur := ""
	for _, r := range s {
		if cells(cur+string(r)) > w {
			out = append(out, cur)
			cur = "  "
		}
		cur += string(r)
	}
	return strings.Join(append(out, cur), "\n")
}

func (s *mountsScreen) View(w, h int) string {
	var b strings.Builder
	switch s.st {
	case mRun:
		return s.run.view(w, h)
	case mRemove:
		return styTitle.Render("移除外部挂载") + "\n\n" + fmt.Sprintf(sRemoveConfirm, s.name.Value()) + "\n\n" + sYNConfirm
	case mReview:
		b.WriteString(styTitle.Render(sReviewTitle) + "\n")
		b.WriteString(summary(s.cfg, s.next, w))
		b.WriteString(issueLines(s.as.Issues, w))
		switch {
		case s.as.Block:
			b.WriteString("\n" + styBad.Render(sReviewBlocked) + "\n")
		case s.as.NeedAck:
			b.WriteString("\n" + styWarn.Render(fmt.Sprintf(sReviewAckNeeded, "I-UNDERSTAND")) + "\n" + s.ack.View() + "\n")
		}
		return b.String()
	case mForm:
		title := map[string]string{"add": "添加外部目录", "extra": "修改外部目录 " + s.name.Value(), "core": "修改 " + s.role + " 路径"}[s.kind]
		b.WriteString(styTitle.Render(title) + "\n\n")
		if s.kind != "core" {
			b.WriteString(sFormName + "：" + s.name.View() + "\n")
		}
		b.WriteString(sFormPath + "：" + s.path.View() + "\n")
		if s.kind != "core" {
			mode := sModeRO
			if !s.ro {
				mode = styWarn.Render(sModeRW)
			}
			mark := " "
			if s.focus == 2 {
				mark = ">"
			}
			b.WriteString(mark + sFormMode + "：" + mode + "\n")
		}
		if len(s.cands) > 1 {
			b.WriteString(stySubtle.Render(truncate(strings.Join(s.cands, "  "), w-2)) + "\n")
		}
		b.WriteString("\n" + issueLines(s.liveMsg, w))
		return b.String()
	}
	if s.err != "" {
		return styBad.Render(sErrPrefix) + s.err
	}
	rows := make([][]string, len(s.rows))
	for i, r := range s.rows {
		mode := "rw"
		if r.Entry.ReadOnly || r.Entry.Role == "" {
			mode = "ro"
		}
		if r.Entry.Role != "extra" {
			mode = "rw"
		}
		lv := "ok"
		for _, is := range r.Issues {
			if is.Sev == mounts.Error {
				lv = "fail"
			} else if is.Sev == mounts.Warn && lv == "ok" {
				lv = "warn"
			}
		}
		fs := r.Probe.FSType
		if fs == "" {
			fs = "-"
		}
		free := "-"
		if r.Probe.TotalBytes > 0 {
			free = fmt.Sprintf("%.0fG", float64(r.Probe.FreeBytes)/(1<<30))
		}
		rows[i] = []string{stripANSI(icon(lv)) + " " + r.Entry.Label(), r.Entry.Host, r.Entry.Container, mode, free, fs}
	}
	start, end := window(s.cur, len(rows), h-6)
	b.WriteString(table([]string{sColName, sColHost, sColContainer, sColMode, sColFree, sColFS}, rows[start:end], []int{11, 22, 24, 4, 6, 8}, s.cur-start))
	if !s.fromFil {
		b.WriteString(stySubtle.Render("（尚无 mounts.json，显示 .env 中的默认目录）") + "\n")
	}
	if len(s.cfg.Extra) == 0 {
		b.WriteString(stySubtle.Render(sNoExtras) + "\n")
	}
	if len(s.rows) > 0 {
		for _, is := range s.rows[s.cur].Issues {
			b.WriteString(truncate(is.String(), w-1) + "\n")
		}
	}
	return b.String()
}

// summary lists the differences between two configs.
func summary(old, next mounts.Config, w int) string {
	var b strings.Builder
	o := map[string]mounts.Entry{}
	for _, e := range old.Entries() {
		o[e.Label()] = e
	}
	seen := map[string]bool{}
	for _, e := range next.Entries() {
		seen[e.Label()] = true
		p, ok := o[e.Label()]
		switch {
		case !ok:
			b.WriteString(fmt.Sprintf("  + %s  %s\n", e.Label(), e.Host))
		case p != e:
			b.WriteString(fmt.Sprintf("  ~ %s  %s -> %s\n", e.Label(), p.Host, e.Host))
		}
	}
	for l, e := range o {
		if !seen[l] {
			b.WriteString(fmt.Sprintf("  - %s  %s\n", l, e.Host))
		}
	}
	if b.Len() == 0 {
		b.WriteString("  （仅重新生成并应用当前 mounts.json）\n")
	}
	return b.String() + "\n"
}

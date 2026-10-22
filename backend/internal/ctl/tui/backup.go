package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type backupScreen struct {
	b     *Backend
	st    int // 0 menu, 1 pick, 2 confirm restore, 3 run
	cur   int
	files []string
	in    textinput.Model
	msg   string
	run   runView
}

func newBackup(b *Backend) *backupScreen { return &backupScreen{b: b, in: newInput()} }

func (s *backupScreen) Title() string { return menuItems[5] }
func (s *backupScreen) Init() tea.Cmd { return nil }
func (s *backupScreen) Close()        { s.run.cancel() }
func (s *backupScreen) Help() string {
	switch s.st {
	case 2:
		return sHelpConfirm
	case 3:
		return sHelpRun
	}
	return sSvcHelp
}

func (s *backupScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	if ok, cmd := s.run.update(msg); ok {
		return s, cmd
	}
	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return s, nil
	}
	key := k.String()
	switch s.st {
	case 3:
		if key == "esc" || s.run.done {
			s.run.cancel()
			s.st, s.cur = 0, 0
		}
		return s, nil
	case 2:
		switch key {
		case "esc":
			s.st = 1
		case "enter":
			if strings.TrimSpace(s.in.Value()) == sRestoreWord {
				file := s.files[s.cur]
				s.st = 3
				return s, s.run.start(s.b.Ctx, "还原 "+filepath.Base(file), func(ctx context.Context, emit func(string)) error {
					return s.b.Restore(ctx, file, emit)
				})
			}
			s.msg = "输入不符"
		default:
			var c tea.Cmd
			s.in, c = s.in.Update(k)
			return s, c
		}
		return s, nil
	}
	n := 3
	if s.st == 1 {
		n = len(s.files)
	}
	switch key {
	case "esc", "q":
		if s.st == 1 {
			s.st, s.cur = 0, 0
			return s, nil
		}
		return s, back
	case "up", "k":
		if s.cur > 0 {
			s.cur--
		}
	case "down", "j":
		if s.cur < n-1 {
			s.cur++
		}
	case "enter":
		if s.st == 1 {
			if len(s.files) == 0 {
				return s, nil
			}
			s.st, s.msg = 2, ""
			s.in.SetValue("")
			s.in.Focus()
			return s, nil
		}
		switch s.cur {
		case 0, 1:
			skip := s.cur == 1
			s.st = 3
			return s, s.run.start(s.b.Ctx, sBackupMenu[s.cur], func(ctx context.Context, emit func(string)) error {
				return s.b.Backup(ctx, skip, emit)
			})
		case 2:
			s.files = s.b.BackupFiles()
			s.st, s.cur = 1, 0
		}
	}
	return s, nil
}

func (s *backupScreen) View(w, h int) string {
	var b strings.Builder
	switch s.st {
	case 3:
		return s.run.view(w, h)
	case 2:
		return styTitle.Render(filepath.Base(s.files[s.cur])) + "\n\n" + styBad.Render(sRestoreWarn) + "\n\n" +
			fmt.Sprintf(sTypeToConfirm, sRestoreWord) + s.in.View() + "\n" + styWarn.Render(s.msg)
	case 1:
		b.WriteString(styTitle.Render(sPickBackup) + "\n")
		if len(s.files) == 0 {
			b.WriteString(sNoBackups + "\n")
		}
		for i, f := range s.files {
			l := "  " + filepath.Base(f)
			if i == s.cur {
				l = stySel.Render(padRight(l, 60))
			}
			b.WriteString(l + "\n")
		}
		return b.String()
	}
	for i, it := range sBackupMenu {
		l := "  " + it
		if i == s.cur {
			l = stySel.Render(padRight(l, 40))
		}
		b.WriteString(l + "\n")
	}
	b.WriteString("\n" + stySubtle.Render(sBackupWarning) + "\n")
	return b.String()
}

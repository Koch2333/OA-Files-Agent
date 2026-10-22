package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type services struct {
	b      *Backend
	cur    int
	st     int // 0 menu, 1 confirm, 2 run
	action string
	in     textinput.Model
	word   string
	msg    string
	run    runView
}

var svcActions = []string{"start", "stop", "restart", "restart-app", "upgrade"}

func newServices(b *Backend) *services {
	in := newInput()
	return &services{b: b, in: in}
}

func (s *services) Title() string { return menuItems[3] }
func (s *services) Init() tea.Cmd { return nil }
func (s *services) Close()        { s.run.cancel() }
func (s *services) Help() string {
	switch s.st {
	case 1:
		return sHelpConfirm
	case 2:
		return sHelpRun
	}
	return sSvcHelp
}

func (s *services) Update(msg tea.Msg) (screen, tea.Cmd) {
	if ok, cmd := s.run.update(msg); ok {
		return s, cmd
	}
	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return s, nil
	}
	key := k.String()
	switch s.st {
	case 2:
		if key == "esc" || (s.run.done && key != "") {
			s.run.cancel()
			s.st = 0
		}
	case 1:
		switch key {
		case "esc":
			s.st = 0
		case "enter":
			if strings.TrimSpace(s.in.Value()) == s.word {
				return s, s.start()
			}
			s.msg = "输入不符"
		default:
			var c tea.Cmd
			s.in, c = s.in.Update(k)
			return s, c
		}
	default:
		switch key {
		case "esc", "q":
			return s, back
		case "up", "k":
			if s.cur > 0 {
				s.cur--
			}
		case "down", "j":
			if s.cur < len(sSvcMenu)-1 {
				s.cur++
			}
		case "enter":
			s.action = svcActions[s.cur]
			s.msg = ""
			switch s.action {
			case "stop":
				s.word = sSvcStopWord
			case "upgrade":
				s.word = sSvcUpWord
			default:
				return s, s.start()
			}
			s.st = 1
			s.in.SetValue("")
			s.in.Focus()
		}
	}
	return s, nil
}

func (s *services) start() tea.Cmd {
	s.st = 2
	act := s.action
	return s.run.start(s.b.Ctx, sSvcMenu[s.cur], func(ctx context.Context, emit func(string)) error {
		return s.b.ServiceAction(ctx, act, emit)
	})
}

func (s *services) View(w, h int) string {
	switch s.st {
	case 2:
		return s.run.view(w, h)
	case 1:
		extra := ""
		if s.action == "upgrade" {
			extra = sSvcUpConfirm + "\n\n"
		}
		return styTitle.Render(sSvcMenu[s.cur]) + "\n\n" + extra + fmt.Sprintf(sTypeToConfirm, s.word) + s.in.View() + "\n" + styWarn.Render(s.msg)
	}
	var b strings.Builder
	for i, it := range sSvcMenu {
		l := "  " + it
		if i == s.cur {
			l = stySel.Render(padRight(l, 40))
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

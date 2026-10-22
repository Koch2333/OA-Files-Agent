package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

const maxLogLines = 5000

type logsScreen struct {
	b      *Backend
	names  []string
	cur    int
	st     int // 0 pick, 1 view, 2 search input
	lines  []string
	vp     viewport.Model
	follow bool
	term   string
	search textinput.Model
	s      *streamRun
	w, h   int
}

func newLogs(b *Backend) *logsScreen {
	return &logsScreen{b: b, names: append([]string{""}, b.S.Inst.Services()...), follow: true, search: newInput(), vp: viewport.New(78, 18)}
}

func (l *logsScreen) Title() string { return menuItems[4] }
func (l *logsScreen) Init() tea.Cmd { return nil }
func (l *logsScreen) Close() {
	if l.s != nil {
		l.s.cancel()
	}
}
func (l *logsScreen) Help() string {
	if l.st == 0 {
		return sSvcHelp
	}
	if l.st == 2 {
		return sHelpConfirm
	}
	return sLogsHelp
}

func (l *logsScreen) render() {
	var b strings.Builder
	for _, ln := range l.lines {
		t := truncate(ln, l.w-1)
		if l.term != "" {
			if i := strings.Index(strings.ToLower(t), strings.ToLower(l.term)); i >= 0 {
				t = t[:i] + stySel.Render(t[i:i+len(l.term)]) + t[i+len(l.term):]
			}
		}
		b.WriteString(t + "\n")
	}
	l.vp.SetContent(b.String())
	if l.follow {
		l.vp.GotoBottom()
	}
}

func (l *logsScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch m := msg.(type) {
	case lineMsg:
		if l.s != nil && m.id == l.s.id {
			l.lines = append(l.lines, m.s)
			if len(l.lines) > maxLogLines {
				l.lines = l.lines[len(l.lines)-maxLogLines:]
			}
			l.render()
			return l, l.s.wait()
		}
		return l, nil
	case doneMsg:
		return l, nil
	case tea.KeyMsg:
		return l.key(m)
	}
	return l, nil
}

func (l *logsScreen) key(k tea.KeyMsg) (screen, tea.Cmd) {
	key := k.String()
	switch l.st {
	case 0:
		switch key {
		case "esc", "q":
			return l, back
		case "up", "k":
			if l.cur > 0 {
				l.cur--
			}
		case "down", "j":
			if l.cur < len(l.names)-1 {
				l.cur++
			}
		case "enter":
			l.st, l.lines = 1, nil
			svc := l.names[l.cur]
			s, cmd := startStream(l.b.Ctx, func(ctx context.Context, emit func(string)) error {
				return l.b.Logs(ctx, svc, 200, emit)
			})
			l.s = s
			return l, cmd
		}
	case 2:
		switch key {
		case "esc":
			l.st = 1
		case "enter":
			l.term = l.search.Value()
			l.st = 1
			l.follow = false
			l.render()
			l.jump(0, 1)
		default:
			var c tea.Cmd
			l.search, c = l.search.Update(k)
			return l, c
		}
	case 1:
		switch key {
		case "esc", "q":
			l.Close()
			l.st = 0
		case "f":
			l.follow = !l.follow
			l.render()
		case "/":
			l.st = 2
			l.search.SetValue(l.term)
			l.search.Focus()
		case "n":
			l.jump(l.vp.YOffset+1, 1)
		case "N":
			l.jump(l.vp.YOffset-1, -1)
		default:
			if key == "up" || key == "pgup" || key == "k" {
				l.follow = false
			}
			var c tea.Cmd
			l.vp, c = l.vp.Update(k)
			return l, c
		}
	}
	return l, nil
}

// jump scrolls to the next line (in direction dir) containing the search term.
func (l *logsScreen) jump(from, dir int) {
	if l.term == "" || len(l.lines) == 0 {
		return
	}
	t := strings.ToLower(l.term)
	for i := from; i >= 0 && i < len(l.lines); i += dir {
		if strings.Contains(strings.ToLower(l.lines[i]), t) {
			l.vp.SetYOffset(i)
			return
		}
	}
}

func (l *logsScreen) View(w, h int) string {
	if l.st == 0 {
		var b strings.Builder
		b.WriteString(styTitle.Render(sLogsPick) + "\n")
		for i, n := range l.names {
			if n == "" {
				n = "(全部服务)"
			}
			x := "  " + n
			if i == l.cur {
				x = stySel.Render(padRight(x, 30))
			}
			b.WriteString(x + "\n")
		}
		return b.String()
	}
	if l.w != w || l.h != h {
		l.w, l.h = w, h
		l.vp.Width, l.vp.Height = w, h-1
		l.render()
	}
	state := sLogsFollow
	if !l.follow {
		state = sLogsPaused
	}
	status := stySubtle.Render("[" + state + "] " + map[bool]string{true: sLogsSearch + l.term, false: ""}[l.term != ""])
	if l.st == 2 {
		status = sLogsSearch + l.search.View()
	}
	return l.vp.View() + "\n" + status
}

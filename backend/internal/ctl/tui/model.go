// Package tui is the bubbletea front end of oa-agentctl. Screens only talk to Backend.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"oa-agent/backend/internal/ctl/cli"
)

const (
	minW = 80
	minH = 24
)

// screen is one menu entry's UI.
type screen interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (screen, tea.Cmd)
	View(w, h int) string
	Help() string
	Title() string
	Close()
}

// backMsg asks the root to return to the main menu.
type backMsg struct{}

func back() tea.Msg { return backMsg{} }

// Model is the root model: the main menu plus at most one active screen.
type Model struct {
	b    *Backend
	w, h int
	cur  screen
	sel  int
	quit bool
}

// New builds the root model.
func New(b *Backend) *Model { return &Model{b: b, w: minW, h: minH} }

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) open(i int) tea.Cmd {
	var s screen
	switch i {
	case 0:
		s = newDashboard(m.b)
	case 1:
		s = newUsers(m.b)
	case 2:
		s = newMountsScreen(m.b)
	case 3:
		s = newServices(m.b)
	case 4:
		s = newLogs(m.b)
	case 5:
		s = newBackup(m.b)
	case 6:
		s = newConfig(m.b)
	case 7:
		s = newDoctor(m.b)
	default:
		return nil
	}
	m.cur = s
	return s.Init()
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = v.Width, v.Height
		return m, nil
	case backMsg:
		if m.cur != nil {
			m.cur.Close()
			m.cur = nil
		}
		return m, nil
	case tea.KeyMsg:
		if v.String() == "ctrl+c" {
			if m.cur != nil {
				m.cur.Close()
			}
			m.quit = true
			return m, tea.Quit
		}
		if m.cur == nil {
			return m.menuKey(v)
		}
	}
	if m.cur != nil {
		var cmd tea.Cmd
		m.cur, cmd = m.cur.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) menuKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch s := k.String(); s {
	case "up", "k":
		if m.sel > 0 {
			m.sel--
		}
	case "down", "j":
		if m.sel < len(menuItems)-1 {
			m.sel++
		}
	case "enter":
		return m, m.open(m.sel)
	case "q", "esc":
		m.quit = true
		return m, tea.Quit
	default:
		if len(s) == 1 && s[0] >= '1' && s[0] <= '8' {
			m.sel = int(s[0] - '1')
			return m, m.open(m.sel)
		}
	}
	return m, nil
}

func (m *Model) View() string {
	if m.quit {
		return ""
	}
	if m.w < minW || m.h < minH {
		return fmt.Sprintf(sTooSmall, m.w, m.h) + "\n"
	}
	title, help := sAppTitle, sHelpMenu
	bodyH := m.h - 2
	var body string
	if m.cur == nil {
		var b strings.Builder
		b.WriteString("\n")
		for i, it := range menuItems {
			l := fmt.Sprintf("  %d. %s", i+1, it)
			if i == m.sel {
				l = stySel.Render(padRight(l, 30))
			}
			b.WriteString(l + "\n")
		}
		b.WriteString("\n" + stySubtle.Render("  安装目录："+m.b.Dir()) + "\n")
		body = b.String()
	} else {
		title += " · " + m.cur.Title()
		help = m.cur.Help()
		body = m.cur.View(m.w, bodyH)
	}
	return fit(styBar.Render(padRight(" "+title, m.w)), body, styBar.Render(padRight(" "+truncate(help, m.w-2), m.w)), m.w, bodyH)
}

// fit pads or clips body to exactly h lines so the help bar stays on the last row.
func fit(top, body, bottom string, w, h int) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	for i := range lines {
		if lipgloss.Width(lines[i]) > w {
			lines[i] = lipgloss.NewStyle().MaxWidth(w).Render(lines[i])
		}
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return top + "\n" + strings.Join(lines, "\n") + "\n" + bottom
}

// Run starts the interactive UI.
func Run(a *cli.App, dir string) error {
	InitColors()
	s, err := a.Open(dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &Backend{S: s, Ctx: ctx, HostIP: "localhost"}
	p := tea.NewProgram(New(b), tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func (b *Backend) timeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(b.Ctx, d)
}

// window returns the slice bounds that keep cursor visible in a list of visible rows.
func window(cursor, total, visible int) (int, int) {
	if visible <= 0 || total <= visible {
		return 0, total
	}
	start := cursor - visible/2
	if start < 0 {
		start = 0
	}
	if start+visible > total {
		start = total - visible
	}
	return start, start + visible
}

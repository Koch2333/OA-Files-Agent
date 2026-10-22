package tui

import (
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
)

var (
	styTitle  = lipgloss.NewStyle().Bold(true)
	stySubtle = lipgloss.NewStyle().Faint(true)
	styGood   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styBad    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	stySel    = lipgloss.NewStyle().Reverse(true)
	styHeader = lipgloss.NewStyle().Bold(true).Underline(true)
	styBar    = lipgloss.NewStyle().Reverse(true)
	styBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 3).BorderForeground(lipgloss.Color("3"))
)

// InitColors honours NO_COLOR (lipgloss does too, this makes it explicit and testable).
func InitColors() {
	if os.Getenv("NO_COLOR") != "" {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
}

// width of s in terminal cells (CJK is double width).
func cells(s string) int { return runewidth.StringWidth(s) }

func padRight(s string, w int) string {
	if n := w - cells(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if cells(s) <= w {
		return s
	}
	return runewidth.Truncate(s, w, "…")
}

// icon returns a status marker usable without colour.
func icon(level string) string {
	switch level {
	case "ok", "healthy", "running":
		return styGood.Render("✔")
	case "warn", "starting":
		return styWarn.Render("!")
	case "info":
		return stySubtle.Render("·")
	default:
		return styBad.Render("✘")
	}
}

// table renders rows with fixed column widths, a header and a highlighted cursor row (-1 = none).
func table(header []string, rows [][]string, widths []int, cursor int) string {
	var b strings.Builder
	line := func(cols []string) string {
		var parts []string
		for i, c := range cols {
			w := widths[i]
			parts = append(parts, padRight(truncate(c, w), w))
		}
		return strings.Join(parts, " ")
	}
	b.WriteString(styHeader.Render(line(header)) + "\n")
	for i, r := range rows {
		l := line(r)
		if i == cursor {
			l = stySel.Render(l)
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

// newInput builds a text input with a static cursor: no blink timers, so SSH sessions stay quiet
// and tests do not wait for timers.
func newInput() textinput.Model {
	in := textinput.New()
	in.Cursor.SetMode(cursor.CursorStatic)
	return in
}

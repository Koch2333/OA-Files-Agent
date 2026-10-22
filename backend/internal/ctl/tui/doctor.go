package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"oa-agent/backend/internal/ctl/doctor"
)

type doctorScreen struct {
	b      *Backend
	id     int
	res    []doctor.Result
	loaded bool
	off    int
}

type doctorMsg struct {
	id  int
	res []doctor.Result
}

func newDoctor(b *Backend) *doctorScreen { return &doctorScreen{b: b, id: newID()} }

func (d *doctorScreen) Title() string { return menuItems[7] }
func (d *doctorScreen) Help() string  { return sDoctorHelp }
func (d *doctorScreen) Close()        { d.id = -1 }
func (d *doctorScreen) Init() tea.Cmd { return d.run() }

func (d *doctorScreen) run() tea.Cmd {
	id := d.id
	d.loaded = false
	return func() tea.Msg {
		ctx, cancel := d.b.timeout(60 * time.Second)
		defer cancel()
		return doctorMsg{id, d.b.Doctor(ctx)}
	}
}

func (d *doctorScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch m := msg.(type) {
	case doctorMsg:
		if m.id == d.id {
			d.res, d.loaded, d.off = m.res, true, 0
		}
	case tea.KeyMsg:
		switch m.String() {
		case "esc", "q":
			return d, back
		case "r":
			return d, d.run()
		case "up", "k":
			if d.off > 0 {
				d.off--
			}
		case "down", "j":
			d.off++
		}
	}
	return d, nil
}

func (d *doctorScreen) View(w, h int) string {
	if !d.loaded {
		return sLoading
	}
	var lines []string
	for _, r := range d.res {
		lv := map[doctor.Level]string{doctor.OK: "ok", doctor.Warn: "warn", doctor.Fail: "fail"}[r.Level]
		lines = append(lines, truncate(fmt.Sprintf("%s %s: %s", icon(lv), r.Name, r.Detail), w))
		if r.Fix != "" && r.Level != doctor.OK {
			lines = append(lines, stySubtle.Render("    修复: "+truncate(r.Fix, w-10)))
		}
	}
	vis := h - 2
	if d.off > len(lines)-vis {
		d.off = len(lines) - vis
	}
	if d.off < 0 {
		d.off = 0
	}
	end := d.off + vis
	if end > len(lines) {
		end = len(lines)
	}
	f, wn := doctor.Counts(d.res)
	return strings.Join(lines[d.off:end], "\n") + "\n" + styTitle.Render(fmt.Sprintf(sDoctorSummary, f, wn))
}

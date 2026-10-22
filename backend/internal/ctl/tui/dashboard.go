package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"oa-agent/backend/internal/ctl/compose"
)

type dashboard struct {
	b      *Backend
	id     int
	svcs   []compose.Service
	disks  []DiskRow
	setup  string
	err    error
	loaded bool
}

type dashMsg struct {
	id    int
	svcs  []compose.Service
	disks []DiskRow
	setup string
	err   error
}

type tickMsg struct{ id int }

// refreshEvery is the dashboard polling interval.
var refreshEvery = 5 * time.Second

func newDashboard(b *Backend) *dashboard { return &dashboard{b: b, id: newID()} }

func (d *dashboard) Init() tea.Cmd { return tea.Batch(d.refresh(), d.tick()) }
func (d *dashboard) Title() string { return menuItems[0] }
func (d *dashboard) Help() string  { return sHelpList }
func (d *dashboard) Close()        { d.id = -1 }

func (d *dashboard) tick() tea.Cmd {
	id := d.id
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{id} })
}

func (d *dashboard) refresh() tea.Cmd {
	id := d.id
	return func() tea.Msg {
		ctx, cancel := d.b.timeout(20 * time.Second)
		defer cancel()
		svcs, err := d.b.Services(ctx)
		m := dashMsg{id: id, svcs: svcs, err: err, disks: d.b.Disks()}
		if err == nil {
			m.setup = d.b.SetupStatus(ctx)
		}
		return m
	}
}

func (d *dashboard) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch m := msg.(type) {
	case dashMsg:
		if m.id == d.id {
			d.svcs, d.disks, d.setup, d.err, d.loaded = m.svcs, m.disks, m.setup, m.err, true
		}
	case tickMsg:
		if m.id == d.id {
			return d, tea.Batch(d.refresh(), d.tick())
		}
	case tea.KeyMsg:
		switch m.String() {
		case "esc", "q":
			return d, back
		case "r":
			return d, d.refresh()
		}
	}
	return d, nil
}

func (d *dashboard) View(w, h int) string {
	var b strings.Builder
	if !d.loaded {
		return sLoading
	}
	b.WriteString(styTitle.Render(sDashServices) + "\n")
	if d.err != nil {
		b.WriteString(styBad.Render(sErrPrefix) + truncate(d.err.Error(), w-6) + "\n")
	}
	rows := make([][]string, 0, len(d.svcs))
	for _, s := range d.svcs {
		st := s.State
		if st == "" {
			st = "missing"
		}
		hl := s.Health
		if hl == "" {
			hl = "-"
		}
		ports := s.Ports
		if ports == "" {
			ports = "-"
		}
		rows = append(rows, []string{icon(s.Effective()) + " " + s.Name, st, hl, ports, s.Uptime()})
	}
	b.WriteString(tableStyled([]string{sColService, sColState, sColHealth, sColPorts, sColUptime}, rows, []int{12, 10, 10, 30, 14}))
	b.WriteString("\n" + styTitle.Render(sDashDisk) + "\n")
	for _, r := range d.disks {
		b.WriteString(fmt.Sprintf("%s %s %s  %s\n", icon(map[string]string{"ok": "ok", "warn": "warn", "error": "fail"}[r.Level]),
			padRight(r.Label, 8), padRight(truncate(r.Host, 38), 38), usage(r.Free, r.Total)))
	}
	b.WriteString("\n" + fmt.Sprintf("%s：%s\n%s：%s\n", sDashSetup, d.setup, sDashURL, d.b.URL()))
	return b.String()
}

func usage(free, total uint64) string {
	if total == 0 {
		return "-"
	}
	used := float64(total-free) / float64(total)
	n := int(used * 10)
	if n > 10 {
		n = 10
	}
	return fmt.Sprintf("[%s%s] 剩余 %.1f GiB / %.1f GiB", strings.Repeat("#", n), strings.Repeat(".", 10-n), float64(free)/(1<<30), float64(total)/(1<<30))
}

// tableStyled is table() for cells that already contain styling (icons): padding uses visible width.
func tableStyled(header []string, rows [][]string, widths []int) string {
	var b strings.Builder
	line := func(cols []string, hdr bool) string {
		var parts []string
		for i, c := range cols {
			if !hdr && cells(stripANSI(c)) > widths[i] {
				c = truncate(stripANSI(c), widths[i])
			}
			pad := widths[i] - cells(stripANSI(c))
			if pad < 0 {
				pad = 0
			}
			parts = append(parts, c+strings.Repeat(" ", pad))
		}
		return strings.Join(parts, " ")
	}
	b.WriteString(styHeader.Render(line(header, true)) + "\n")
	for _, r := range rows {
		b.WriteString(line(r, false) + "\n")
	}
	return b.String()
}

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			in = true
		case in && r == 'm':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

package tui

import (
	"context"
	"strings"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"
)

type lineMsg struct {
	id int
	s  string
}

type doneMsg struct {
	id  int
	err error
}

var nextID int64

func newID() int { return int(atomic.AddInt64(&nextID, 1)) }

type streamRun struct {
	id     int
	ch     chan tea.Msg
	cancel context.CancelFunc
}

// startStream runs fn in a goroutine and feeds its lines to the UI one message at a time.
func startStream(parent context.Context, fn func(ctx context.Context, emit func(string)) error) (*streamRun, tea.Cmd) {
	ctx, cancel := context.WithCancel(parent)
	s := &streamRun{id: newID(), ch: make(chan tea.Msg, 1024), cancel: cancel}
	go func() {
		defer close(s.ch)
		emit := func(l string) {
			select {
			case s.ch <- lineMsg{s.id, l}:
			case <-ctx.Done():
			}
		}
		err := fn(ctx, emit)
		select {
		case s.ch <- doneMsg{s.id, err}:
		case <-ctx.Done():
		}
	}()
	return s, s.wait()
}

func (s *streamRun) wait() tea.Cmd {
	return func() tea.Msg {
		m, ok := <-s.ch
		if !ok {
			return nil
		}
		return m
	}
}

// runView shows the progress of one streamed operation.
type runView struct {
	title   string
	lines   []string
	running bool
	done    bool
	err     error
	s       *streamRun
	note    string // extra line shown after completion (e.g. rollback notice)
}

func (r *runView) start(ctx context.Context, title string, fn func(ctx context.Context, emit func(string)) error) tea.Cmd {
	r.cancel()
	*r = runView{title: title, running: true}
	s, cmd := startStream(ctx, fn)
	r.s = s
	return cmd
}

func (r *runView) cancel() {
	if r.s != nil {
		r.s.cancel()
	}
}

// update consumes stream messages of this run; it returns whether the message was ours.
func (r *runView) update(msg tea.Msg) (bool, tea.Cmd) {
	if r.s == nil {
		return false, nil
	}
	switch m := msg.(type) {
	case lineMsg:
		if m.id != r.s.id {
			return false, nil
		}
		r.lines = append(r.lines, m.s)
		if len(r.lines) > 2000 {
			r.lines = r.lines[len(r.lines)-2000:]
		}
		return true, r.s.wait()
	case doneMsg:
		if m.id != r.s.id {
			return false, nil
		}
		r.running, r.done, r.err = false, true, m.err
		return true, nil
	}
	return false, nil
}

func (r *runView) view(w, h int) string {
	var b strings.Builder
	b.WriteString(styTitle.Render(r.title) + "\n")
	max := h - 3
	if max < 1 {
		max = 1
	}
	lines := r.lines
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	for _, l := range lines {
		b.WriteString(truncate(l, w) + "\n")
	}
	switch {
	case r.running:
		b.WriteString(stySubtle.Render(sRunning))
	case r.err != nil:
		b.WriteString(styBad.Render(sFailed+"：") + truncate(r.err.Error(), w-8))
	case r.done:
		b.WriteString(styGood.Render(sDone))
	}
	if r.note != "" {
		b.WriteString("\n" + styWarn.Render(r.note))
	}
	return b.String()
}

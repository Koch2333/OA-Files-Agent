package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"oa-agent/backend/internal/ctl/envfile"
)

const (
	cList = iota
	cEdit
	cDomain
	cDomainConfirm
	cRun
)

type configScreen struct {
	b    *Backend
	rows [][2]string
	cur  int
	st   int
	in   textinput.Model
	msg  string
	fqdn string
	warn string
	run  runView
}

func newConfig(b *Backend) *configScreen {
	c := &configScreen{b: b, in: newInput()}
	c.rows = b.ConfigRows()
	return c
}

func (c *configScreen) Title() string { return menuItems[6] }
func (c *configScreen) Init() tea.Cmd { return nil }
func (c *configScreen) Close()        { c.run.cancel() }
func (c *configScreen) Help() string {
	switch c.st {
	case cEdit, cDomain, cDomainConfirm:
		return sHelpConfirm
	case cRun:
		return sHelpRun
	}
	return sConfigHelp
}

func editable(key string) bool {
	return !errors.Is(envfile.ValidateConfig(key, "x"), envfile.ErrNotWhitelisted)
}

func (c *configScreen) Update(msg tea.Msg) (screen, tea.Cmd) {
	if ok, cmd := c.run.update(msg); ok {
		return c, cmd
	}
	k, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return c, nil
	}
	key := k.String()
	switch c.st {
	case cRun:
		if key == "esc" || c.run.done {
			c.run.cancel()
			c.st = cList
			c.rows = c.b.ConfigRows()
		}
		return c, nil
	case cEdit:
		switch key {
		case "esc":
			c.st, c.msg = cList, ""
		case "enter":
			name := c.rows[c.cur][0]
			if err := c.b.ConfigSet(name, c.in.Value()); err != nil {
				c.msg = err.Error()
				return c, nil
			}
			c.msg = fmt.Sprintf(sConfigSaved, name)
			c.st = cList
			c.rows = c.b.ConfigRows()
		default:
			var cmd tea.Cmd
			c.in, cmd = c.in.Update(k)
			c.msg = ""
			if err := envfile.ValidateConfig(c.rows[c.cur][0], c.in.Value()); err != nil {
				c.msg = err.Error()
			}
			return c, cmd
		}
		return c, nil
	case cDomain:
		switch key {
		case "esc":
			c.st, c.msg = cList, ""
		case "enter":
			fq := strings.TrimSpace(c.in.Value())
			plan, err := c.b.PlanDomain(fq)
			if err != nil {
				c.msg = err.Error()
				return c, nil
			}
			c.fqdn, c.msg = fq, ""
			c.warn = sDomainWarn
			if !plan.InvalidatesKeys {
				c.warn = "切换会重建全部容器。" + sDomainWarn
			}
			c.st = cDomainConfirm
			c.in.SetValue("")
		default:
			var cmd tea.Cmd
			c.in, cmd = c.in.Update(k)
			return c, cmd
		}
		return c, nil
	case cDomainConfirm:
		switch key {
		case "esc":
			c.st = cList
		case "enter":
			if strings.TrimSpace(c.in.Value()) == sDomainWord {
				c.st = cRun
				fq := c.fqdn
				return c, c.run.start(c.b.Ctx, "域名 "+fq, func(ctx context.Context, emit func(string)) error {
					return c.b.ApplyDomain(ctx, fq, emit)
				})
			}
			c.msg = "输入不符"
		default:
			var cmd tea.Cmd
			c.in, cmd = c.in.Update(k)
			return c, cmd
		}
		return c, nil
	}
	switch key {
	case "esc", "q":
		return c, back
	case "up", "k":
		if c.cur > 0 {
			c.cur--
		}
	case "down", "j":
		if c.cur < len(c.rows)-1 {
			c.cur++
		}
	case "e", "enter":
		if len(c.rows) == 0 {
			return c, nil
		}
		name := c.rows[c.cur][0]
		if !editable(name) {
			c.msg = sConfigLocked
			return c, nil
		}
		c.st, c.msg = cEdit, ""
		v := c.rows[c.cur][1]
		if envfile.IsSecret(name) {
			v = ""
		}
		c.in.SetValue(v)
		c.in.Focus()
	case "d":
		c.st, c.msg = cDomain, ""
		c.in.SetValue("")
		c.in.Focus()
	}
	return c, nil
}

func (c *configScreen) View(w, h int) string {
	switch c.st {
	case cRun:
		return c.run.view(w, h)
	case cEdit:
		return styTitle.Render(c.rows[c.cur][0]) + "\n\n" + sConfigEdit + c.in.View() + "\n" + styWarn.Render(c.msg)
	case cDomain:
		return styTitle.Render("域名 / HTTPS") + "\n\n" + sDomainPrompt + c.in.View() + "\n" + styWarn.Render(c.msg)
	case cDomainConfirm:
		return styTitle.Render("切换到 "+orHTTP(c.fqdn)) + "\n\n" + styWarn.Render(c.warn) + "\n\n" + fmt.Sprintf(sTypeToConfirm, sDomainWord) + c.in.View() + "\n" + styWarn.Render(c.msg)
	}
	var b strings.Builder
	rows := make([][]string, len(c.rows))
	for i, r := range c.rows {
		mark := " "
		if editable(r[0]) {
			mark = "*"
		}
		rows[i] = []string{mark + r[0], r[1]}
	}
	start, end := window(c.cur, len(rows), h-4)
	b.WriteString(table([]string{"键 (* 可修改)", "值"}, rows[start:end], []int{28, w - 31}, c.cur-start))
	if c.msg != "" {
		b.WriteString("\n" + styWarn.Render(truncate(c.msg, w-2)))
	}
	return b.String()
}

func orHTTP(f string) string {
	if f == "" {
		return "HTTP"
	}
	return f
}

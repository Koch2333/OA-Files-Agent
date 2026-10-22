package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"oa-agent/backend/internal/ctl/oaadmin"
)

const (
	uList = iota
	uConfirm
	uInput
	uBusy
	uSecret
	uResult
)

type users struct {
	b      *Backend
	id     int
	list   []oaadmin.User
	loaded bool
	cur    int
	st     int
	action string
	target string
	word   string // typed confirmation word ("" = y/n)
	in     textinput.Model
	msg    string
	secret oaadmin.Secret
	secFor string
}

type usersMsg struct {
	id   int
	list []oaadmin.User
	err  error
}

type userActMsg struct {
	id     int
	action string
	user   string
	secret *oaadmin.Secret
	out    string
	err    error
}

func newUsers(b *Backend) *users {
	in := newInput()
	in.CharLimit = 64
	return &users{b: b, id: newID(), in: in}
}

func (u *users) Title() string { return menuItems[1] }
func (u *users) Close()        { u.id = -1; u.secret = oaadmin.Secret{} }
func (u *users) Init() tea.Cmd { return u.load() }

func (u *users) Help() string {
	switch u.st {
	case uConfirm:
		if u.word != "" {
			return sHelpConfirm
		}
		return sYNConfirm
	case uSecret:
		return sSecretHint
	case uInput:
		return sHelpForm
	}
	return sUsersHelp
}

func (u *users) load() tea.Cmd {
	id := u.id
	return func() tea.Msg {
		ctx, cancel := u.b.timeout(30 * time.Second)
		defer cancel()
		l, err := u.b.Users(ctx)
		return usersMsg{id, l, err}
	}
}

func (u *users) run(action, user string) tea.Cmd {
	id := u.id
	u.st = uBusy
	return func() tea.Msg {
		ctx, cancel := u.b.timeout(60 * time.Second)
		defer cancel()
		switch action {
		case "reset-password":
			s, err := u.b.ResetPassword(ctx, user)
			return userActMsg{id: id, action: action, user: user, secret: &s, err: err}
		case "create-admin":
			s, err := u.b.CreateAdmin(ctx, user)
			return userActMsg{id: id, action: action, user: user, secret: &s, err: err}
		}
		out, err := u.b.UserAction(ctx, action, user)
		return userActMsg{id: id, action: action, user: user, out: out, err: err}
	}
}

func (u *users) ask(action, target string) {
	u.action, u.target, u.st = action, target, uConfirm
	u.word = ""
	switch action {
	case "reset-password", "disable-user", "reset-mfa":
		u.word = target
	}
	u.in.SetValue("")
	u.in.Placeholder = ""
	u.in.Focus()
}

func (u *users) Update(msg tea.Msg) (screen, tea.Cmd) {
	switch m := msg.(type) {
	case usersMsg:
		if m.id != u.id {
			return u, nil
		}
		u.loaded = true
		u.list = m.list
		if m.err != nil {
			u.msg = fmt.Sprintf(sUsersUnavail, m.err)
		} else if u.st == uList {
			u.msg = ""
		}
		if u.cur >= len(u.list) {
			u.cur = 0
		}
		return u, nil
	case userActMsg:
		if m.id != u.id {
			return u, nil
		}
		if m.err != nil {
			u.st, u.msg = uResult, sErrPrefix+m.err.Error()
			return u, nil
		}
		if m.secret != nil {
			u.secret, u.secFor, u.st = *m.secret, m.user, uSecret
			return u, nil
		}
		u.st, u.msg = uResult, fmt.Sprintf(sActionDone, m.action+" "+m.user)
		if m.out != "" {
			u.msg += " — " + m.out
		}
		return u, u.load()
	case tea.KeyMsg:
		return u.key(m)
	}
	return u, nil
}

func (u *users) key(k tea.KeyMsg) (screen, tea.Cmd) {
	s := k.String()
	switch u.st {
	case uSecret:
		// any key dismisses; the password is wiped from memory here
		u.secret, u.secFor, u.st, u.msg = oaadmin.Secret{}, "", uList, ""
		return u, u.load()
	case uBusy:
		return u, nil
	case uResult:
		u.st = uList
		return u, nil
	case uConfirm:
		if s == "esc" {
			u.st = uList
			return u, nil
		}
		if u.word == "" {
			switch s {
			case "y", "Y":
				return u, u.run(u.action, u.target)
			case "n", "N":
				u.st = uList
			}
			return u, nil
		}
		if s == "enter" {
			if strings.TrimSpace(u.in.Value()) == u.word {
				return u, u.run(u.action, u.target)
			}
			u.msg = "输入不符"
			return u, nil
		}
		var c tea.Cmd
		u.in, c = u.in.Update(k)
		return u, c
	case uInput:
		switch s {
		case "esc":
			u.st = uList
			return u, nil
		case "enter":
			name := strings.TrimSpace(u.in.Value())
			if err := oaadmin.ValidUsername(name); err != nil {
				u.msg = err.Error()
				return u, nil
			}
			u.msg = ""
			u.ask("create-admin", name)
			return u, nil
		}
		var c tea.Cmd
		u.in, c = u.in.Update(k)
		return u, c
	}
	switch s {
	case "esc", "q":
		return u, back
	case "up", "k":
		if u.cur > 0 {
			u.cur--
		}
	case "down", "j":
		if u.cur < len(u.list)-1 {
			u.cur++
		}
	case "r":
		return u, u.load()
	case "a":
		u.st = uInput
		u.in.SetValue("")
		u.in.Focus()
	case "p", "m", "u", "d":
		if len(u.list) == 0 {
			return u, nil
		}
		act := map[string]string{"p": "reset-password", "m": "reset-mfa", "u": "unlock", "d": "disable-user"}[s]
		u.msg = ""
		u.ask(act, u.list[u.cur].Username)
	}
	return u, nil
}

func (u *users) View(w, h int) string {
	var b strings.Builder
	switch u.st {
	case uSecret:
		return u.secretView(w)
	case uBusy:
		return sRunning
	case uConfirm:
		b.WriteString(styTitle.Render(u.action+" "+u.target) + "\n\n")
		b.WriteString(map[string]string{
			"reset-password": fmt.Sprintf(sConfirmReset, u.target),
			"disable-user":   fmt.Sprintf(sConfirmDisable, u.target),
			"reset-mfa":      fmt.Sprintf(sConfirmMFA, u.target),
			"unlock":         fmt.Sprintf(sConfirmUnlock, u.target),
			"create-admin":   fmt.Sprintf(sConfirmCreate, u.target),
		}[u.action] + "\n\n")
		if u.word != "" {
			b.WriteString(fmt.Sprintf(sTypeToConfirm, u.word) + u.in.View() + "\n")
		} else {
			b.WriteString(sYNConfirm + "\n")
		}
		if u.msg != "" {
			b.WriteString(styWarn.Render(u.msg) + "\n")
		}
		return b.String()
	case uInput:
		b.WriteString(sNewAdminPrompt + u.in.View() + "\n")
		if u.msg != "" {
			b.WriteString(styWarn.Render(u.msg) + "\n")
		}
		return b.String()
	}
	if !u.loaded {
		return sLoading
	}
	if len(u.list) == 0 {
		b.WriteString(sNoUsers + "\n")
	} else {
		rows := make([][]string, len(u.list))
		for i, x := range u.list {
			rows[i] = []string{x.Username, x.Role, x.Status, yesNo(x.MFA), yesNo(x.Locked)}
		}
		start, end := window(u.cur, len(rows), h-5)
		b.WriteString(table([]string{sColUser, sColRole, sColStatus, sColMFA, sColLocked}, rows[start:end], []int{24, 12, 12, 6, 6}, u.cur-start))
	}
	if u.msg != "" {
		b.WriteString("\n" + styWarn.Render(truncate(u.msg, w-2)) + "\n")
	}
	return b.String()
}

func yesNo(v bool) string {
	if v {
		return sYes
	}
	return sNo
}

// secretView renders the one-time password in a box with copy-friendly spacing (no wrapping, no ANSI inside the value).
func (u *users) secretView(w int) string {
	var inner string
	if u.secret.Password != "" {
		inner = styTitle.Render(sSecretTitle) + "\n\n  " + u.secFor + "\n\n  " + u.secret.Password + "\n"
	} else {
		inner = styWarn.Render(sSecretUnparsed) + "\n\n" + strings.TrimSpace(u.secret.Raw) + "\n"
	}
	return "\n" + styBox.Render(inner) + "\n\n" + styWarn.Render(sSecretHint) + "\n"
}

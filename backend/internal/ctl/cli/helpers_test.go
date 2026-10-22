package cli

import "oa-agent/backend/internal/ctl/envfile"

type envfileAlias = envfile.File

func (a *App) getuidForTest(uid int) { a.Getuid = func() int { return uid } }

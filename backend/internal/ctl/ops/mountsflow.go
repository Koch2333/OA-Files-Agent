package ops

import (
	"context"

	"oa-agent/backend/internal/ctl/mounts"
)

// Assessment is everything the operator must see before a mounts change is applied.
type Assessment struct {
	Issues      []mounts.Issue
	Fixes       []mounts.Fix // owner/mkdir repairs for the changed entries
	MissingDirs bool         // some core directory does not exist yet
	Block       bool         // an error finding: the change cannot be applied
	NeedAck     bool         // data-relocation risk: explicit acknowledgement required
}

// changedEntries returns the entries of next that are new or differ from prev.
func changedEntries(prev, next mounts.Config) []mounts.Entry {
	old := map[string]mounts.Entry{}
	for _, e := range prev.Entries() {
		old[e.Label()] = e
	}
	var out []mounts.Entry
	for _, e := range next.Entries() {
		if o, ok := old[e.Label()]; !ok || o != e {
			out = append(out, e)
		}
	}
	return out
}

// AssessMounts validates a proposed config against the current one: rules, filesystem checks of
// the changed entries and data-safety risks. It reads the filesystem and the container list only.
func (e Env) AssessMounts(ctx context.Context, prev, next mounts.Config) Assessment {
	var a Assessment
	opt := mounts.Options{InstallDir: e.P.Inst.Dir}
	a.Issues = append(a.Issues, mounts.Validate(next, opt)...)
	prober := e.Prober
	if prober == nil {
		prober = mounts.OSProber{}
	}
	if !mounts.HasErrors(a.Issues) {
		for _, ent := range changedEntries(prev, next) {
			r := mounts.Check(ent, prober)
			a.Issues = append(a.Issues, r.Issues...)
			a.Fixes = append(a.Fixes, r.Fixes...)
			for _, is := range r.Issues {
				if is.Code == "missing" && ent.Role != "extra" {
					a.MissingDirs = true
				}
			}
		}
		var running []string
		if changed := mounts.Changes(prev, next); contains(changed, "pgdata") {
			running, _ = e.P.Running(ctx)
		}
		a.Issues = append(a.Issues, mounts.Risks(mounts.RiskInput{Old: prev, New: next, Prober: prober, RunningService: running})...)
	}
	a.Block = mounts.HasErrors(a.Issues)
	a.NeedAck = mounts.NeedsAck(a.Issues)
	return a
}

// RunFixes executes the fixes (root only; the caller checks), reporting each command.
func RunFixes(fixes []mounts.Fix, pr Progress) error {
	for _, f := range fixes {
		pr.say("执行：%s", f.Command())
		if err := mounts.RunFix(f); err != nil {
			return err
		}
	}
	return nil
}

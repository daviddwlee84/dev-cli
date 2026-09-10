package cli

import (
	"fmt"
	"github.com/daviddwlee84/dev-cli/internal/triage"
)

func (w *tuiWorkflow) runSkillManagement() error {
	w.result.Scoped = true
	run, err := runSkillManage(w.ctx, &w.app, skillManageRequest{Refs: w.request.RepoRefs, Selected: w.request.SkillSelected, Scope: w.request.SkillScope, Action: w.request.SkillAction})
	w.result.RefreshSkills = true
	completed, failed := 0, 0
	for _, outcome := range run.Receipt.Outcomes {
		if outcome.Status == "completed" {
			completed++
		} else {
			failed++
		}
	}
	w.result.Status = fmt.Sprintf("Skills · %d completed · %d require attention", completed, failed)
	if !run.Mutated {
		w.result.Status = "Returned from skills management"
	}
	if failed > 0 {
		w.result.Severity = "error"
	}
	if run.Mutated {
		var touched []triage.Item
		for _, target := range run.Targets {
			touched = append(touched, triage.Item{Kind: "repo", RepositoryPath: target.RepoPath, RepositoryID: target.CommonDir})
		}
		if len(touched) > 0 {
			delta, refreshErr := refreshTriageScope(w.ctx, &w.app, w.request, touched)
			w.result.Local = &delta
			if err == nil {
				err = refreshErr
			}
		}
	}
	if w.app.canPick() {
		_, pauseErr := newPrompter(&w.app).line("Enter to return to dashboard", "")
		if err == nil {
			err = pauseErr
		}
	}
	return err
}

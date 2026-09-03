package inventory

import (
	"context"
	"errors"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/agentskill"
	"github.com/daviddwlee84/dev-cli/internal/agenttarget"
)

// AgentSkillOptions selects native scopes and the explicit remote freshness
// pass. With neither scope selected, project and global rows are both returned.
type AgentSkillOptions struct {
	Project bool
	Global  bool
	Check   bool
	Limiter *Limiter
}

// CollectAgentSkills scans each repository target through the shared local
// process limiter, scans global paths once, and only then groups remote source
// checks across the complete result.
func CollectAgentSkills(ctx context.Context, targets []agenttarget.Target, options AgentSkillOptions) (agentskill.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	project, global := options.Project, options.Global
	if !project && !global {
		project, global = true, true
	}
	targets = agenttarget.Dedupe(targets)
	partCount := 0
	if project {
		partCount += len(targets)
	}
	globalIndex := -1
	if global {
		globalIndex = partCount
		partCount++
	}
	parts := make([]agentskill.Result, partCount)
	errs := make([]error, partCount)
	limiter := options.Limiter
	if limiter == nil {
		limiter = NewLimiter(8)
	}

	var wait sync.WaitGroup
	launch := func(index int, scan func() (agentskill.Result, error)) {
		wait.Go(func() {
			release, ok := limiter.Acquire(ctx)
			if !ok {
				errs[index] = ctx.Err()
				return
			}
			defer release()
			parts[index], errs[index] = scan()
		})
	}
	if project {
		for index, target := range targets {
			target := target
			launch(index, func() (agentskill.Result, error) {
				return agentskill.Scan(ctx, []agenttarget.Target{target}, agentskill.ListOptions{Project: true})
			})
		}
	}
	if global {
		launch(globalIndex, func() (agentskill.Result, error) {
			return agentskill.Scan(ctx, nil, agentskill.ListOptions{Global: true})
		})
	}
	wait.Wait()

	result := agentskill.MergeResults(parts...)
	if options.Check && ctx.Err() == nil {
		result.Skills = agentskill.CheckUpdates(ctx, result.Skills)
	}
	joined := errors.Join(errs...)
	if ctxErr := ctx.Err(); ctxErr != nil {
		joined = errors.Join(joined, ctxErr)
	}
	return result, joined
}

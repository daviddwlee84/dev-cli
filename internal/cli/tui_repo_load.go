package cli

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/daviddwlee84/dev-cli/internal/inventory"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/tui"
)

type tuiRepoJob struct {
	index    int
	validate bool
	row      tui.RepoRow
}

type tuiRepoDone struct {
	job     tuiRepoJob
	row     tui.RepoRow
	err     error
	proceed chan bool
}

type tuiRepoRecord struct {
	row      tui.RepoRow
	complete bool
}

// collectTUIRepoStream owns admission, deduplication and publication. Only one
// cancellable permit request is outstanding; there is never a goroutine per
// queued repository. A slow favorite occupies its own slot, not a phase barrier.
func collectTUIRepoStream(ctx context.Context, metadata tuiRepoCatalog, repositories *tuiFuture[[]repo.Repo], live *tuiFuture[tuiRuntimeSnapshot], limiter *inventory.Limiter,
	validate func(context.Context, repo.Repo) (repo.Repo, error), collect func(context.Context, tui.RepoRow) tui.RepoRow,
	tasksErr error, publish func(string, tui.RepoRow)) ([]tui.RepoRow, error) {
	var records []tuiRepoRecord
	var favorites, ordinary []tuiRepoJob
	reserved := map[string]int{}
	known := map[string]bool{}
	owners := map[string]int{}
	for _, row := range metadata.seeds {
		index := len(records)
		records = append(records, tuiRepoRecord{row: row})
		favorites = append(favorites, tuiRepoJob{index: index, validate: true, row: row})
		for _, key := range tuiRepoKeys(row.Repo) {
			reserved[key]++
		}
	}
	var pending []repo.Repo
	admitDiscovered := func() {
		waiting := pending[:0]
		for _, r := range pending {
			keys := tuiRepoKeys(r)
			duplicate, held := false, false
			for _, key := range keys {
				duplicate = duplicate || known[key]
				held = held || reserved[key] > 0
			}
			if duplicate {
				for _, key := range keys {
					if known[key] {
						owner := owners[key]
						if preferTUIRepoAlias(&records[owner].row, r) && records[owner].complete {
							publish("enrichment", records[owner].row)
						}
						break
					}
				}
				continue
			}
			if held {
				waiting = append(waiting, r)
				continue
			}
			row := tui.RepoRow{Repo: r, Asset: metadata.asset(r)}
			job := tuiRepoJob{index: len(records), row: row}
			records = append(records, tuiRepoRecord{row: row})
			for _, key := range keys {
				known[key], owners[key] = true, job.index
			}
			if row.Favorite() {
				favorites = append(favorites, job)
			} else {
				ordinary = append(ordinary, job)
			}
		}
		pending = waiting
	}

	completed := make(chan tuiRepoDone, 8)
	permits := make(chan func())
	var workers sync.WaitGroup
	workerCtx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); workers.Wait() }()
	var err error
	active, acquiring := 0, false
	discoveryReady, runtimeReady := repositories.ready, live.ready
	var runtimeSnapshot tuiRuntimeSnapshot
	var runtimeErr error
	runtimeDone := false
	join := func(row *tui.RepoRow) {
		row.Context.TaskErr = tasksErr
		if row.Unavailable != "" {
			return
		}
		row.Pending = "runtime pending"
		if runtimeDone {
			if runtimeErr != nil {
				row.Context.RuntimeErr = runtimeErr
				row.Pending = "runtime unavailable"
			} else {
				joinRuntime(row, runtimeSnapshot)
				row.Pending = ""
			}
		}
		if row.Favorite() && row.Repo.HasGit && !row.Repo.Bare && !row.GitKnown {
			// Status failure is not proof that this navigation target disappeared.
			// Keep observation-dependent actions pending; open revalidates identity.
			row.Pending = "Git observation unavailable"
		}
	}
	for {
		if ctx.Err() != nil {
			return nil, errors.Join(err, ctx.Err())
		}
		if !acquiring && len(favorites)+len(ordinary) > 0 {
			acquiring = true
			workers.Go(func() {
				release, ok := limiter.Acquire(workerCtx)
				if !ok {
					return
				}
				select {
				case permits <- release:
				case <-workerCtx.Done():
					release()
				}
			})
		}
		if discoveryReady == nil && runtimeDone && active == 0 && !acquiring && len(favorites)+len(ordinary) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(err, ctx.Err())
		case <-discoveryReady:
			discoveryReady = nil
			err = errors.Join(err, repositories.err)
			pending = append(pending, repositories.value...)
			admitDiscovered()
		case <-runtimeReady:
			runtimeReady, runtimeDone = nil, true
			runtimeSnapshot, runtimeErr = live.value, live.err
			err = errors.Join(err, runtimeErr)
			for i := range records {
				if records[i].complete && records[i].row.Repo.Path != "" && records[i].row.Unavailable == "" {
					join(&records[i].row)
					publish("runtime", records[i].row)
				}
			}
		case release := <-permits:
			acquiring = false
			var job tuiRepoJob
			if len(favorites) > 0 {
				job, favorites = favorites[0], favorites[1:]
			} else {
				job, ordinary = ordinary[0], ordinary[1:]
			}
			active++
			workers.Go(func() {
				defer release()
				result := tuiRepoDone{job: job, row: job.row}
				if job.validate {
					result.row.Repo, result.err = validate(workerCtx, job.row.Repo)
					if result.err == nil {
						// Keep the admitted slot through validation and enrichment.
						// Only identity arbitration returns to the dispatcher.
						result.proceed = make(chan bool, 1)
						select {
						case completed <- result:
						case <-workerCtx.Done():
							return
						}
						select {
						case proceed := <-result.proceed:
							if !proceed {
								return
							}
						case <-workerCtx.Done():
							return
						}
						result.proceed, result.job.validate = nil, false
						result.row = collect(workerCtx, result.row)
					}
				} else {
					result.row = collect(workerCtx, job.row)
				}
				select {
				case completed <- result:
				case <-workerCtx.Done():
				}
			})
		case result := <-completed:
			if result.proceed == nil {
				active--
			}
			index, row := result.job.index, result.row
			if result.job.validate {
				for _, key := range tuiRepoKeys(result.job.row.Repo) {
					reserved[key]--
				}
				if result.err != nil {
					row = result.job.row
					row.Pending, row.Unavailable = "unavailable", result.err.Error()
					err = errors.Join(err, fmt.Errorf("favorite %s: %w", row.Repo.Path, result.err))
					// A replaced checkout at this path must not overwrite the
					// unavailable favorite with an apparently clean observation.
					for path := range canonicalRepoValues(row.Repo.Path) {
						known["path:"+path], owners["path:"+path] = true, index
					}
					records[index] = tuiRepoRecord{row: row, complete: true}
					publish("enrichment", row)
				} else {
					keys, duplicate := tuiRepoKeys(row.Repo), false
					for _, key := range keys {
						duplicate = duplicate || known[key]
					}
					if duplicate {
						records[index] = tuiRepoRecord{}
						active--
						result.proceed <- false
					} else {
						for _, key := range keys {
							known[key], owners[key] = true, index
						}
						records[index].row = row
						result.proceed <- true
					}
				}
				admitDiscovered()
				continue
			}
			if row.Favorite() && row.Repo.HasGit && !row.Repo.Bare && !row.GitKnown {
				reason := "repository Git observation unavailable"
				if main, ok := row.Context.Main(); ok && main.StatusErr != nil {
					reason = main.StatusErr.Error()
				}
				err = errors.Join(err, fmt.Errorf("favorite %s: %s", row.Repo.Path, reason))
			}
			preferTUIRepoAlias(&row, records[index].row.Repo)
			join(&row)
			records[index] = tuiRepoRecord{row: row, complete: true}
			publish("enrichment", row)
		}
	}
	rows := make([]tui.RepoRow, 0, len(records))
	for _, record := range records {
		if record.complete && record.row.Repo.Path != "" {
			rows = append(rows, record.row)
		}
	}
	return rows, err
}

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/daviddwlee84/dev-cli/internal/catalog"
	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/note"
	"github.com/daviddwlee84/dev-cli/internal/repo"
	"github.com/daviddwlee84/dev-cli/internal/tui"
	"github.com/spf13/cobra"
)

func newNoteCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note",
		Short: "Capture and search timestamped repository thoughts",
		Long: `Store multiple quick thoughts outside the repository as ordinary Markdown
files keyed by the repository's stable catalog ID. The configured
paths.state_dir/notes directory (default $XDG_DATA_HOME/dev/notes) is durable
truth; SQLite under $XDG_CACHE_HOME/dev is only a rebuildable FTS search index.

Use n in the TUI for quick add and N for the notes overlay.`,
	}
	cmd.AddCommand(
		newNoteAddCmd(app), newNoteListCmd(app), newNoteShowCmd(app),
		newNoteSearchCmd(app), newNoteEditCmd(app), newNoteDeleteCmd(app),
		newNotePathCmd(app), newNoteReindexCmd(app),
	)
	return cmd
}

func newNoteAddCmd(app *App) *cobra.Command {
	var (
		repoRef       string
		tags          []string
		editor        bool
		editorCommand string
	)
	cmd := &cobra.Command{
		Use:   "add [thought...]",
		Short: "Append one thought to a repository",
		Long: `Create one timestamped Markdown note. Run inside a repo, or pass --repo.
Text arguments form a quick one-line thought; --editor opens a temporary body
and atomically saves it only after the editor exits with a non-empty result.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			entry, _, err := noteRepository(ctxOf(), app, repoRef)
			if err != nil {
				return err
			}
			body := strings.TrimSpace(strings.Join(args, " "))
			if editor {
				body, err = editNoteBody(body, editorCommand)
				if err != nil {
					return err
				}
			}
			if body == "" {
				return fmt.Errorf("note body is empty; provide text or use --editor")
			}
			n, err := app.Notes.Add(ctxOf(), entry.ID, entry.Title(), body, tags)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "%s  %s\n  %s\n", n.ID, n.Repository, n.Preview(100))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&repoRef, "repo", "r", "", "repository (default: repo containing cwd)")
	f.StringArrayVarP(&tags, "tag", "t", nil, "tag (repeatable)")
	f.BoolVarP(&editor, "editor", "e", false, "compose the body in VISUAL/EDITOR")
	f.StringVar(&editorCommand, "editor-command", "", "editor command override")
	return cmd
}

func newNoteListCmd(app *App) *cobra.Command {
	var (
		all     bool
		jsonOut bool
		tag     string
	)
	cmd := &cobra.Command{
		Use:   "list [repo]",
		Short: "List notes newest-first",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repositoryID := ""
			if !all {
				ref := ""
				if len(args) == 1 {
					ref = args[0]
				}
				entry, _, err := noteRepository(ctxOf(), app, ref)
				if err != nil {
					return err
				}
				repositoryID = entry.ID
			} else if len(args) > 0 {
				return fmt.Errorf("repo argument and --all are alternatives")
			}
			notes, err := app.Notes.List(repositoryID)
			if err != nil {
				return err
			}
			if tag != "" {
				filtered := notes[:0]
				for _, n := range notes {
					if containsString(n.Tags, strings.ToLower(tag)) {
						filtered = append(filtered, n)
					}
				}
				notes = filtered
			}
			return renderNotes(app, notes, jsonOut)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&all, "all", "a", false, "all repositories")
	f.BoolVar(&jsonOut, "json", false, "emit JSON")
	f.StringVarP(&tag, "tag", "t", "", "only notes with this tag")
	return cmd
}

func newNoteShowCmd(app *App) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show <note-id>",
		Short: "Show one complete note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := resolveNote(app, args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(app.Out).Encode(n)
			}
			fmt.Fprintf(app.Out, "%s\nrepo     %s\ncreated  %s\nupdated  %s\ntags     %s\npath     %s\n\n%s",
				n.ID, n.Repository, n.Created.Format(time.RFC3339), n.Updated.Format(time.RFC3339),
				dash(strings.Join(n.Tags, ", ")), config.Contract(n.Path), n.Body)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteSearchCmd(app *App) *cobra.Command {
	var (
		repoRef string
		jsonOut bool
		limit   int
	)
	cmd := &cobra.Command{
		Use:   "search <query...>",
		Short: "Full-text search note body, tags, and repository",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repositoryID := ""
			if repoRef != "" {
				entry, _, err := noteRepository(ctxOf(), app, repoRef)
				if err != nil {
					return err
				}
				repositoryID = entry.ID
			}
			notes, err := app.Notes.Search(strings.Join(args, " "), repositoryID, limit)
			if err != nil {
				return err
			}
			return renderNotes(app, notes, jsonOut)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&repoRef, "repo", "r", "", "scope to this repository")
	f.BoolVar(&jsonOut, "json", false, "emit JSON")
	f.IntVar(&limit, "limit", 100, "maximum matches")
	return cmd
}

func newNoteEditCmd(app *App) *cobra.Command {
	var (
		tags          []string
		editorCommand string
	)
	cmd := &cobra.Command{
		Use:   "edit <note-id>",
		Short: "Edit one note body safely in VISUAL/EDITOR",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := resolveNote(app, args[0])
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("tag") {
				tags = n.Tags
			}
			updated, err := editExistingNote(app, n, tags, editorCommand)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "updated %s\n  %s\n", updated.ID, updated.Preview(100))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&tags, "tag", "t", nil, "replace tags (repeatable; omit to preserve)")
	f.StringVar(&editorCommand, "editor", "", "editor command override")
	return cmd
}

func newNoteDeleteCmd(app *App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <note-id>",
		Aliases: []string{"rm"},
		Short:   "Delete one note after confirmation",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := resolveNote(app, args[0])
			if err != nil {
				return err
			}
			if !yes {
				if !app.interactive() {
					return fmt.Errorf("refusing to delete note non-interactively without --yes")
				}
				if !confirm(app, bufio.NewReader(app.In), "delete note "+n.ID[:8]+" — "+n.Preview(60)) {
					fmt.Fprintln(app.Out, "not changed")
					return nil
				}
			}
			if err := app.Notes.Delete(ctxOf(), n.ID); err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "deleted %s\n", n.ID)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not prompt")
	return cmd
}

func newNotePathCmd(app *App) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "path [repo]",
		Short: "Print the durable Markdown note path",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("repo argument and --all are alternatives")
			}
			if all {
				fmt.Fprintln(app.Out, app.Notes.Store.Path(""))
				return nil
			}
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			entry, _, err := noteRepository(ctxOf(), app, ref)
			if err != nil {
				return err
			}
			fmt.Fprintln(app.Out, app.Notes.Store.Path(entry.ID))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "print the notes root")
	return cmd
}

func newNoteReindexCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "reindex",
		Short: "Rebuild the disposable SQLite FTS index from Markdown notes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := app.Notes.Reindex()
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "indexed %d note(s) into %s\n", n, config.Contract(app.Cfg.NotesIndexFile()))
			return nil
		},
	}
}

func noteRepository(ctx context.Context, app *App, ref string) (*catalog.Entry, repo.Repo, error) {
	if ref != "" {
		// A stable catalog ID is accepted anywhere a repo name is.
		if entry, err := app.Registry.Get(ref); err == nil {
			if entry.Kind != catalog.KindRepository {
				return nil, repo.Repo{}, fmt.Errorf("catalog asset %s is %s, not a repository", ref, entry.Kind)
			}
			location, ok := entry.LocationFor(config.Hostname())
			if !ok || location.CurrentPath == "" {
				return nil, repo.Repo{}, fmt.Errorf("catalog repository %s has no path on this host", ref)
			}
			r := repo.Repo{Name: entry.Name, Path: location.CurrentPath,
				RealPath: location.RealPath, CommonDir: location.GitCommonDir, HasGit: true}
			return entry, r, nil
		}
		resolved, _, err := resolveRepoRef(app, ref)
		if err != nil {
			return nil, repo.Repo{}, err
		}
		return ensureNoteRepository(ctx, app, tui.NoteTarget{Repo: resolved})
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, repo.Repo{}, err
	}
	g, err := gitx.Discover(ctx, cwd)
	if err != nil {
		return nil, repo.Repo{}, fmt.Errorf("%s is not in a repository; pass --repo", config.Contract(cwd))
	}
	r := repo.Repo{Name: g.Name, Path: g.MainRoot, RealPath: g.MainRoot,
		CommonDir: g.GitCommonDir, MainRoot: g.MainRoot, HasGit: true}
	return ensureNoteRepository(ctx, app, tui.NoteTarget{Repo: r})
}

func ensureNoteRepository(ctx context.Context, app *App, target tui.NoteTarget) (*catalog.Entry, repo.Repo, error) {
	r := target.Repo
	if target.CatalogID != "" {
		if entry, err := app.Registry.Get(target.CatalogID); err == nil {
			if entry.Kind != catalog.KindRepository {
				return nil, r, fmt.Errorf("catalog asset %s is %s, not a repository", entry.ID, entry.Kind)
			}
			return entry, r, nil
		}
	}
	if r.RealPath == "" || r.CommonDir == "" {
		if g, err := gitx.Discover(ctx, r.Path); err == nil {
			// Notes belong to the clone's canonical repository, never to a
			// disposable linked-worktree path.
			r.Path, r.RealPath, r.MainRoot = g.MainRoot, g.MainRoot, g.MainRoot
			if r.Name == "" {
				r.Name = g.Name
			}
			r.CommonDir = g.GitCommonDir
		}
	}
	observation := catalog.Observation{
		Host: config.Hostname(), Path: r.Path, RealPath: r.RealPath,
		CommonDir: r.CommonDir, Name: r.Name,
		RemoteIdentity: gitx.RemoteFromConfig(r.CommonDir, "origin"),
	}
	var entry *catalog.Entry
	err := app.Catalog.WithLock(ctx, func() error {
		var ensureErr error
		entry, ensureErr = app.Registry.EnsureRepository(observation)
		if ensureErr == nil && entry.Kind != catalog.KindRepository {
			ensureErr = fmt.Errorf("catalog asset %s is %s, not a repository", entry.ID, entry.Kind)
		}
		return ensureErr
	})
	return entry, r, err
}

func resolveNote(app *App, ref string) (*note.Note, error) {
	ref = strings.TrimSpace(ref)
	if len(ref) < 8 {
		return nil, fmt.Errorf("note ID/prefix must be at least 8 characters")
	}
	if n, err := app.Notes.Get(ref); err == nil {
		return n, nil
	}
	notes, err := app.Notes.List("")
	if err != nil {
		return nil, err
	}
	var hits []*note.Note
	for _, n := range notes {
		if strings.HasPrefix(n.ID, ref) {
			hits = append(hits, n)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return nil, fmt.Errorf("%s: %w", ref, note.ErrNotFound)
	default:
		return nil, fmt.Errorf("note prefix %q is ambiguous (%d matches)", ref, len(hits))
	}
}

func renderNotes(app *App, notes []*note.Note, jsonOut bool) error {
	if jsonOut {
		enc := json.NewEncoder(app.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(notes)
	}
	if len(notes) == 0 {
		fmt.Fprintln(app.Out, "No notes.")
		return nil
	}
	t := NewTable("ID", "WHEN", "REPO", "TAGS", "THOUGHT")
	for _, n := range notes {
		t.Add(n.ID[:8], humanAge(time.Since(n.Created)), n.Repository,
			dash(strings.Join(n.Tags, ",")), truncate(n.Preview(80), 80))
	}
	t.Render(app.Out)
	return nil
}

func editExistingNote(app *App, n *note.Note, tags []string, editorOverride string) (*note.Note, error) {
	tmp, err := os.CreateTemp("", "dev-note-edit-*.md")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	if _, err := io.WriteString(tmp, n.Body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	proc, chosen, err := editorProcess(path, editorOverride)
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	proc.Stdin, proc.Stdout, proc.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := proc.Run(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("editor %q: %w", chosen, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read edited body (preserved at %s): %w", path, err)
	}
	if strings.TrimSpace(string(body)) == "" {
		_ = os.Remove(path)
		return nil, fmt.Errorf("edited note body is empty; not saved")
	}
	updated, err := app.Notes.Update(ctxOf(), n.ID, n.Revision(), string(body), tags)
	if err != nil {
		return nil, fmt.Errorf("save edited note: %w; edited body preserved at %s", err, path)
	}
	_ = os.Remove(path)
	return updated, nil
}

func editNoteBody(initial, editorOverride string) (string, error) {
	tmp, err := os.CreateTemp("", "dev-note-*.md")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := io.WriteString(tmp, initial); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if !strings.HasSuffix(initial, "\n") {
		_, _ = io.WriteString(tmp, "\n")
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	proc, chosen, err := editorProcess(path, editorOverride)
	if err != nil {
		return "", err
	}
	proc.Stdin, proc.Stdout, proc.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := proc.Run(); err != nil {
		return "", fmt.Errorf("editor %q: %w", chosen, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(body)) == "" {
		return "", fmt.Errorf("edited note body is empty; not saved")
	}
	return string(body), nil
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

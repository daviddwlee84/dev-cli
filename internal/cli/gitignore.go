package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/gitx"
	"github.com/daviddwlee84/dev-cli/internal/ignore"
	"github.com/spf13/cobra"
)

func newGitignoreCmd(app *App) *cobra.Command {
	var (
		stdout    bool
		offline   bool
		noOS      bool
		noEditors bool
		noAgents  bool
		noEnv     bool
		list      bool
	)
	cmd := &cobra.Command{
		Use:     "gitignore [language...]",
		Aliases: []string{"ignore"},
		Short:   "Write a .gitignore from GitHub's templates plus the entries every repo needs",
		Long: `Compose a .gitignore for this repository.

Language sections come from GitHub's published templates, fetched once and
cached. On top of those, dev adds the entries no language template covers but
almost every repository wants: the host platform's junk files, editor state,
local env files, and the directories coding-agent harnesses create — a
harness's linked worktree left untracked makes every git status in the main
checkout unreadable.

With no language argument, the languages are inferred from the files present.

Everything dev writes goes inside a delimited block, so re-running updates that
block and leaves rules you added by hand alone.

  dev gitignore                 # detect from the repo's files
  dev gitignore python node     # explicit
  dev gitignore --stdout        # preview without writing
  dev gitignore --no-agents     # skip the harness section`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := ctxOf()

			if list {
				fmt.Fprintln(app.Out, "Available without a network:")
				for _, n := range ignore.BundledNames() {
					fmt.Fprintf(app.Out, "  %s\n", n)
				}
				fmt.Fprintln(app.Out, "\nAny name from https://github.com/github/gitignore works online.")
				return nil
			}

			// Operate on the repository root, not wherever the shell happens
			// to be: a .gitignore in a subdirectory only covers that subtree.
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			target := cwd
			if g, err := gitx.Discover(ctx, cwd); err == nil {
				target = g.Root
			} else if !stdout {
				return fmt.Errorf("%s is not a git repository (use --stdout to preview anyway)",
					config.Contract(cwd))
			}

			names := args
			if len(names) == 0 {
				names = ignore.Detect(target)
				if len(names) == 0 {
					app.warnf("no language detected in %s — writing only the common sections",
						config.Contract(target))
				} else {
					fmt.Fprintf(app.Err, "detected: %s\n", strings.Join(names, ", "))
				}
			}

			fetcher := ignore.NewFetcher(filepath.Join(config.CacheHome(), "dev", "gitignore"))
			fetcher.Offline = offline

			var sections []ignore.Section
			for _, n := range names {
				s, err := fetcher.Get(ctx, n)
				if err != nil {
					// One unknown language must not lose the rest of the file.
					app.warnf("%v", err)
					continue
				}
				sections = append(sections, s)
			}

			extras := ignore.DefaultExtras()
			extras.OS = !noOS
			extras.Editors = !noEditors
			extras.Agents = !noAgents
			extras.Env = !noEnv

			block := ignore.Compose(sections, extras)
			if stdout {
				fmt.Fprint(app.Out, block)
				return nil
			}

			path := filepath.Join(target, ".gitignore")
			existing, _ := os.ReadFile(path)
			updated := ignore.Merge(string(existing), block)
			if string(existing) == updated {
				fmt.Fprintf(app.Out, "%s is already up to date\n", config.Contract(path))
				return nil
			}
			if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
				return err
			}

			verb := "wrote"
			if ignore.HasManagedBlock(string(existing)) {
				verb = "updated the managed block in"
			} else if len(existing) > 0 {
				verb = "appended a managed block to"
			}
			fmt.Fprintf(app.Out, "%s %s\n", verb, config.Contract(path))
			for _, s := range sections {
				fmt.Fprintf(app.Out, "   %-14s %s\n", s.Name, s.Source)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&stdout, "stdout", false, "print instead of writing the file")
	f.BoolVar(&offline, "offline", false, "use only cached and bundled templates")
	f.BoolVar(&list, "list", false, "list the templates available offline")
	f.BoolVar(&noOS, "no-os", false, "omit the host platform's junk files")
	f.BoolVar(&noEditors, "no-editors", false, "omit editor and IDE state")
	f.BoolVar(&noAgents, "no-agents", false, "omit coding-agent harness directories")
	f.BoolVar(&noEnv, "no-env", false, "omit local env and secret files")
	cmd.ValidArgsFunction = completeGitignoreNames
	return cmd
}

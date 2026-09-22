package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/daviddwlee84/dev-cli/internal/config"
	"github.com/daviddwlee84/dev-cli/internal/desktop"
	"github.com/daviddwlee84/dev-cli/internal/forge"
	"github.com/daviddwlee84/dev-cli/internal/picker"
	"github.com/daviddwlee84/dev-cli/internal/privatefile"
	"github.com/daviddwlee84/dev-cli/internal/safefile"
	"github.com/daviddwlee84/dev-cli/internal/snippet"
	"github.com/spf13/cobra"
)

func snippetServiceFor(app *App) *snippet.Service {
	if app.snippetService != nil {
		return app.snippetService
	}
	return forge.NewSnippetService()
}

func newSnippetCmd(app *App, githubOnly bool) *cobra.Command {
	name, summary := "snippet", "Find and share GitHub Gists and GitLab snippets"
	if githubOnly {
		name, summary = "gist", "Find and share GitHub Gists"
	}
	cmd := &cobra.Command{Use: name, Short: summary, Long: summary + `. Lists belong to the authenticated accounts, not the current Git checkout.
GitLab --project selects an explicit project. Ordinary search matches metadata;
--content explicitly downloads file contents. A GitHub secret Gist is readable
by anyone with its link; a GitLab private personal snippet is owner-only.

Create accepts named text files or '-' for stdin. With no files in a terminal,
it opens an editor and reviews the destination before publishing. Publishing
never retries an uncertain request. See dev help snippets.`}
	cmd.AddCommand(newSnippetListCmd(app, githubOnly, false), newSnippetListCmd(app, githubOnly, true), newSnippetOpenCmd(app, githubOnly), newSnippetCreateCmd(app, githubOnly))
	return cmd
}

func snippetScopeFlags(cmd *cobra.Command, githubOnly bool, provider, project *string, listing bool) {
	fallback := ""
	if listing {
		fallback = "all"
	}
	if githubOnly {
		*provider = "github"
	} else {
		cmd.Flags().StringVar(provider, "forge", fallback, "platform: github or gitlab (list/search also accept all)")
		cmd.Flags().StringVar(project, "project", "", "explicit GitLab project path or numeric ID")
		choices := []string{"github", "gitlab"}
		if listing {
			choices = append(choices, "all")
		}
		registerFlagCompletion(cmd, "forge", fixedCompletions(choices...))
	}
}

func snippetScope(provider, project string, listing bool) (snippet.Kind, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if project != "" {
		if provider == "" || provider == "all" {
			provider = "gitlab"
		}
		if provider != "gitlab" {
			return "", fmt.Errorf("--project requires GitLab")
		}
	}
	switch provider {
	case "github", "gitlab":
		return snippet.Kind(provider), nil
	case "", "all":
		if listing {
			return snippet.Kind("all"), nil
		}
		if provider == "" {
			return "", nil
		}
	}
	return "", fmt.Errorf("choose --forge github or gitlab%s", map[bool]string{true: " (or all for listing)"}[listing])
}

func newSnippetListCmd(app *App, githubOnly, search bool) *cobra.Command {
	var provider, project string
	var content, jsonOut bool
	use, short := "list [query...]", "List the authenticated accounts' snippets"
	argsCheck := cobra.ArbitraryArgs
	if search {
		use, short, argsCheck = "search query...", "Search snippet descriptions and filenames", cobra.MinimumNArgs(1)
	}
	cmd := &cobra.Command{Use: use, Short: short, Args: argsCheck, RunE: func(cmd *cobra.Command, args []string) error {
		kind, err := snippetScope(provider, project, true)
		if err != nil {
			return err
		}
		query := strings.Join(args, " ")
		if content && strings.TrimSpace(query) == "" {
			return fmt.Errorf("--content requires a search query")
		}
		result, listErr := snippetServiceFor(app).List(cmd.Context(), snippet.ListOptions{Forge: kind, Project: project, Query: query, Content: content})
		if err := renderSnippetList(app, result, jsonOut); err != nil {
			return err
		}
		return listErr
	}}
	if !search {
		cmd.Aliases = []string{"ls"}
	}
	snippetScopeFlags(cmd, githubOnly, &provider, &project, true)
	cmd.Flags().BoolVar(&content, "content", false, "also search file contents (bounded network reads; reports incomplete coverage)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit metadata and coverage as JSON; never include file contents")
	return cmd
}

func renderSnippetList(app *App, result snippet.ListResult, jsonOut bool) error {
	if jsonOut {
		return writeSnippetJSON(app.Out, result)
	}
	table := app.newTable("PLATFORM", "ID", "TITLE / DESCRIPTION", "FILES", "VISIBILITY", "PROJECT", "UPDATED", "URL")
	for _, item := range result.Items {
		names := make([]string, 0, len(item.Files))
		for _, file := range item.Files {
			names = append(names, file.Name)
		}
		title := item.Title
		if title == "" {
			title = item.Description
		}
		updated := "—"
		if !item.UpdatedAt.IsZero() {
			updated = item.UpdatedAt.Format("2006-01-02")
		}
		table.Add(string(item.Forge), snippetDisplay(item.ID), truncate(snippetDisplay(title), 65), truncate(snippetDisplay(strings.Join(names, ", ")), 55), snippetDisplay(item.Visibility), dash(snippetDisplay(item.Project)), updated, snippetDisplay(item.URL))
	}
	if len(result.Items) == 0 {
		fmt.Fprintln(app.Out, "No matching snippets in the observed results.")
	} else {
		table.Render(app.Out)
	}
	if !result.Complete {
		fmt.Fprintln(app.Err, "Snippet coverage is incomplete; unread or unavailable entries may also match.")
	}
	for _, issue := range result.Issues {
		fmt.Fprintf(app.Err, "%s: %s\n", snippetDisplay(string(issue.Forge)), snippetDisplay(issue.Message))
	}
	return nil
}

func newSnippetOpenCmd(app *App, githubOnly bool) *cobra.Command {
	var provider, project string
	var printOnly bool
	cmd := &cobra.Command{Use: "open [URL|platform:ID|ID]", Short: "Open a snippet, or choose one from your inventory", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		kind, err := snippetScope(provider, project, false)
		if err != nil {
			return err
		}
		service := snippetServiceFor(app)
		var item snippet.Item
		if len(args) == 0 {
			if !app.canPick() {
				return fmt.Errorf("provide a snippet URL or platform:ID when no interactive picker is available")
			}
			result, listErr := service.List(cmd.Context(), snippet.ListOptions{Forge: kind, Project: project})
			if len(result.Items) == 0 {
				if listErr != nil {
					return listErr
				}
				return fmt.Errorf("no snippets available")
			}
			if listErr != nil {
				app.warnf("snippet inventory is incomplete: %v", listErr)
			}
			candidates := make([]picker.Item, 0, len(result.Items))
			for index, row := range result.Items {
				title := row.Title
				if title == "" {
					title = row.Description
				}
				candidates = append(candidates, picker.Item{Value: fmt.Sprint(index), Label: string(row.Forge) + ":" + row.ID + " " + snippetDisplay(title), Description: snippetDisplay(row.Visibility + " · " + row.Project)})
			}
			chosen, used, pickErr := app.pick(cmd.Context(), picker.Request{Prompt: "Open snippet", Items: candidates})
			if pickErr != nil {
				return pickErr
			}
			if !used {
				return fmt.Errorf("no interactive picker available")
			}
			found := false
			for i, candidate := range candidates {
				if chosen.Item.Value == candidate.Value {
					item = result.Items[i]
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("picker returned an unknown snippet")
			}
		} else {
			hosts := map[snippet.Kind]string{}
			for _, p := range service.Providers() {
				hosts[p.Kind()] = p.Host()
			}
			identity, parseErr := snippet.ParseReference(args[0], kind, hosts)
			if parseErr != nil {
				return parseErr
			}
			if project != "" {
				identity.Project = project
			}
			item, err = service.Get(cmd.Context(), identity)
			if err != nil {
				return err
			}
		}
		fmt.Fprintln(app.Out, item.URL)
		if printOnly {
			return nil
		}
		return openSnippetURL(cmd.Context(), app, item.URL)
	}}
	snippetScopeFlags(cmd, githubOnly, &provider, &project, false)
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the verified snippet URL without opening a browser")
	return cmd
}

func openSnippetURL(ctx context.Context, app *App, url string) error {
	if app.snippetOpenURL != nil {
		return app.snippetOpenURL(ctx, url)
	}
	return desktop.OpenURL(ctx, url)
}

type snippetCreateFlags struct {
	provider, project, title, description, visibility, filename, editor string
	dryRun, web, jsonOut                                                bool
}

type snippetCreatePreview struct {
	SchemaVersion int                  `json:"schema_version"`
	Outcome       string               `json:"outcome"`
	Forge         snippet.Kind         `json:"forge"`
	Host          string               `json:"host"`
	Account       string               `json:"account,omitempty"`
	Project       string               `json:"project,omitempty"`
	Title         string               `json:"title,omitempty"`
	Description   string               `json:"description,omitempty"`
	Visibility    string               `json:"visibility"`
	Files         []snippetFilePreview `json:"files"`
	Draft         string               `json:"draft,omitempty"`
}
type snippetFilePreview struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
}

func newSnippetCreateCmd(app *App, githubOnly bool) *cobra.Command {
	var flags snippetCreateFlags
	cmd := &cobra.Command{Use: "create [file...]", Aliases: []string{"new"}, Short: "Publish text files, stdin or an editor draft as one snippet", Args: cobra.ArbitraryArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return runSnippetCreate(cmd.Context(), app, args, flags)
	}}
	snippetScopeFlags(cmd, githubOnly, &flags.provider, &flags.project, false)
	f := cmd.Flags()
	f.StringVar(&flags.title, "title", "", "snippet title (GitLab; also used as a GitHub description fallback)")
	f.StringVarP(&flags.description, "description", "d", "", "description")
	f.StringVar(&flags.visibility, "visibility", "", "github: secret/public; gitlab: private/public/internal (if supported)")
	f.StringVar(&flags.filename, "filename", "", "filename for stdin or the editor draft")
	f.StringVar(&flags.editor, "editor", "", "editor command override for a new draft (otherwise VISUAL/EDITOR)")
	f.BoolVar(&flags.dryRun, "dry-run", false, "preview filenames and destination without publishing content")
	f.BoolVar(&flags.web, "web", false, "open the created snippet after its URL is recorded")
	f.BoolVar(&flags.jsonOut, "json", false, "emit a metadata-only preview or publication result as JSON")
	return cmd
}

func runSnippetCreate(ctx context.Context, app *App, args []string, flags snippetCreateFlags) error {
	kind, err := snippetScope(flags.provider, flags.project, false)
	if err != nil {
		return err
	}
	if flags.editor != "" && len(args) > 0 {
		return fmt.Errorf("--editor is for a new draft; do not combine it with input files")
	}
	fromStdin := false
	for _, arg := range args {
		if arg == "-" {
			if fromStdin {
				return fmt.Errorf("stdin may be selected only once")
			}
			fromStdin = true
		}
	}
	if flags.filename != "" && len(args) > 0 && !fromStdin {
		return fmt.Errorf("--filename names stdin or an editor draft, not existing files")
	}
	interactiveCreate := !flags.jsonOut && app.interactive() && !fromStdin && (len(args) == 0 || kind == "")
	if kind == "" && !interactiveCreate {
		return fmt.Errorf("select --forge github or gitlab (or use dev gist create)")
	}
	if len(args) == 0 && !interactiveCreate {
		return fmt.Errorf("provide text files, or '-' with --filename for stdin; editor drafts require a terminal")
	}
	p := newPrompter(app)
	if interactiveCreate && kind == "" {
		chosen, e := p.choiceOf("Platform", "github", []string{"github", "gitlab"}, map[string]string{"github": "github", "gitlab": "gitlab"})
		if e != nil {
			return e
		}
		kind = snippet.Kind(chosen)
	}
	if interactiveCreate && kind == snippet.GitLab && flags.project == "" {
		flags.project, err = p.line("GitLab project (blank for personal)", "")
		if err != nil {
			return err
		}
	}
	req := snippet.CreateRequest{Forge: kind, Project: flags.project, Title: flags.title, Description: flags.description, Visibility: flags.visibility}
	for _, provider := range snippetServiceFor(app).Providers() {
		if provider.Kind() == kind {
			req.Host = provider.Host()
			break
		}
	}
	draft := ""
	var draftObserved fs.FileInfo
	if len(args) == 0 {
		if flags.filename == "" {
			flags.filename, err = p.line("Filename", "snippet.txt")
			if err != nil {
				return err
			}
		}
		if err = validateSnippetFilename(flags.filename); err != nil {
			return err
		}
		draft, err = createSnippetDraft(app, flags.filename)
		if err != nil {
			return err
		}
		fmt.Fprintf(app.Out, "Draft: %s\n", config.Contract(draft))
		process, chosen, e := editorProcess(draft, flags.editor)
		if e == nil {
			process.Stdin, process.Stdout, process.Stderr = app.In, app.Out, app.Err
			e = process.Run()
		}
		if e != nil {
			return fmt.Errorf("editor %q failed; draft retained at %s: %w", chosen, config.Contract(draft), e)
		}
		body, observed, e := readSnippetFileIdentity(ctx, draft)
		draftObserved = observed
		if e != nil {
			return fmt.Errorf("read draft retained at %s: %w", config.Contract(draft), e)
		}
		req.Files = []snippet.InputFile{{Name: flags.filename, Content: body}}
	} else {
		req.Files, err = readSnippetInputs(ctx, app.In, args, flags.filename)
		if err != nil {
			return err
		}
	}
	retained := func(cause error) error {
		if draft != "" {
			return fmt.Errorf("%w; draft retained at %s", cause, config.Contract(draft))
		}
		return cause
	}
	if interactiveCreate {
		if kind == snippet.GitLab && req.Title == "" {
			req.Title, err = p.line("Title", req.Files[0].Name)
			if err != nil {
				return retained(err)
			}
		}
		if req.Description == "" {
			req.Description, err = p.line("Description (optional)", "")
			if err != nil {
				return retained(err)
			}
		}
		if req.Visibility == "" {
			choices, fallback := []string{"secret", "public"}, "secret"
			if kind == snippet.GitLab {
				choices, fallback = []string{"private", "public", "internal"}, "private"
			}
			accepted := map[string]string{}
			for _, choice := range choices {
				accepted[choice] = choice
			}
			req.Visibility, err = p.choiceOf("Visibility", fallback, choices, accepted)
			if err != nil {
				return retained(err)
			}
		}
	}
	req, err = snippet.ValidateCreate(req)
	if err != nil {
		return retained(err)
	}
	if interactiveCreate && !flags.dryRun {
		req.ExpectedOwner, err = snippetServiceFor(app).Account(ctx, req.Forge)
		if err != nil {
			return retained(err)
		}
	}
	preview := snippetPreview(req, draft)
	if flags.dryRun {
		preview.Outcome = "preview"
		if flags.jsonOut {
			return writeSnippetJSON(app.Out, preview)
		}
		renderSnippetPreview(app, preview)
		return nil
	}
	if interactiveCreate {
		renderSnippetPreview(app, preview)
		yes, e := p.confirm("Publish this snippet?", false)
		if e != nil {
			return retained(e)
		}
		if !yes {
			fmt.Fprintln(app.Out, "Canceled; nothing was published.")
			if draft != "" {
				fmt.Fprintf(app.Out, "Draft retained at %s\n", config.Contract(draft))
			}
			return nil
		}
	}
	result, publishErr := snippetServiceFor(app).Create(ctx, req)
	if result.Outcome == snippet.Created && draft != "" {
		if e := removeUnchangedSnippetDraft(ctx, draft, draftObserved, req.Files[0].Content); e != nil {
			app.warnf("snippet created; draft retained at %s: %v", config.Contract(draft), e)
		} else {
			draft = ""
		}
	}
	if flags.jsonOut {
		receipt := struct {
			snippet.CreateResult
			Draft string `json:"draft,omitempty"`
		}{CreateResult: result}
		receipt.Draft = draft
		if e := writeSnippetJSON(app.Out, receipt); e != nil {
			return retained(errors.Join(publishErr, e))
		}
	} else if result.Outcome == "created" {
		fmt.Fprintln(app.Out, result.Item.URL)
	}
	if result.Outcome != "created" {
		if publishErr == nil {
			publishErr = fmt.Errorf("snippet publication outcome: %s", result.Outcome)
		}
		if result.Outcome == "unknown" {
			app.warnf("publication outcome is unknown; inspect dev snippet list before deciding to create again")
		}
		return retained(publishErr)
	}
	if publishErr != nil {
		app.warnf("snippet created: %v", publishErr)
	}
	if flags.web {
		if e := openSnippetURL(ctx, app, result.Item.URL); e != nil {
			app.warnf("snippet created at %s; browser did not open: %v", result.Item.URL, e)
		}
	}
	return nil
}

func snippetPreview(req snippet.CreateRequest, draft string) snippetCreatePreview {
	out := snippetCreatePreview{SchemaVersion: 1, Outcome: "preview", Forge: req.Forge, Host: req.Host, Account: req.ExpectedOwner, Project: req.Project, Title: req.Title, Description: req.Description, Visibility: req.Visibility, Draft: draft}
	for _, file := range req.Files {
		out.Files = append(out.Files, snippetFilePreview{Name: file.Name, Bytes: len(file.Content)})
	}
	return out
}
func renderSnippetPreview(app *App, p snippetCreatePreview) {
	fmt.Fprintf(app.Out, "Publish to %s (%s)\n", p.Forge, snippetDisplay(p.Host))
	if p.Account != "" {
		fmt.Fprintf(app.Out, "  account     %s\n", snippetDisplay(p.Account))
	}
	if p.Project != "" {
		fmt.Fprintf(app.Out, "  project     %s\n", snippetDisplay(p.Project))
	} else if p.Forge == snippet.GitLab {
		fmt.Fprintln(app.Out, "  scope       personal")
	}
	fmt.Fprintf(app.Out, "  visibility  %s\n", p.Visibility)
	if p.Forge == snippet.GitHub && p.Visibility == "secret" {
		fmt.Fprintln(app.Out, "  access      anyone with the link can read")
	}
	if p.Forge == snippet.GitLab && p.Visibility == "private" {
		if p.Project == "" {
			fmt.Fprintln(app.Out, "  access      only your account")
		} else {
			fmt.Fprintln(app.Out, "  access      authorized project members")
		}
	}
	if p.Title != "" {
		fmt.Fprintf(app.Out, "  title       %s\n", snippetDisplay(p.Title))
	}
	if p.Description != "" {
		fmt.Fprintf(app.Out, "  description %s\n", snippetDisplay(p.Description))
	}
	for _, file := range p.Files {
		fmt.Fprintf(app.Out, "  file        %s (%d bytes)\n", snippetDisplay(file.Name), file.Bytes)
	}
	if p.Draft != "" {
		fmt.Fprintf(app.Out, "  draft       %s\n", config.Contract(p.Draft))
	}
}
func readSnippetInputs(ctx context.Context, in io.Reader, paths []string, stdinName string) ([]snippet.InputFile, error) {
	var files []snippet.InputFile
	total := 0
	for _, path := range paths {
		name := filepath.Base(path)
		var body []byte
		var err error
		if path == "-" {
			name = stdinName
			if name == "" {
				return nil, fmt.Errorf("stdin requires --filename")
			}
			body, err = io.ReadAll(io.LimitReader(in, int64(snippet.MaxCreateBytes)+1))
		} else {
			body, err = readSnippetFile(ctx, path)
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", snippetDisplay(path), err)
		}
		total += len(body)
		if int64(total) > snippet.MaxCreateBytes {
			return nil, fmt.Errorf("snippet inputs exceed %d bytes", snippet.MaxCreateBytes)
		}
		if err = validateSnippetFilename(name); err != nil {
			return nil, err
		}
		files = append(files, snippet.InputFile{Name: name, Content: body})
	}
	return files, nil
}
func readSnippetFile(ctx context.Context, path string) ([]byte, error) {
	body, _, err := readSnippetFileIdentity(ctx, path)
	return body, err
}
func readSnippetFileIdentity(ctx context.Context, path string) ([]byte, fs.FileInfo, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	return safefile.ReadStableRegular(ctx, root, filepath.Base(absolute), nil, snippet.MaxCreateBytes)
}

// Cleanup only the exact editor draft we published. An external editor can
// continue saving during remote I/O; newer contents belong to the user.
func removeUnchangedSnippetDraft(ctx context.Context, path string, observed fs.FileInfo, published []byte) error {
	root, _, err := safefile.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(path)
	body, current, err := safefile.ReadStableRegular(ctx, root, name, observed, snippet.MaxCreateBytes)
	if err != nil {
		return err
	}
	if !safefile.SameFileState(observed, current) || !bytes.Equal(body, published) {
		return safefile.ErrChanged
	}
	latest, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !safefile.SameFileState(current, latest) {
		return safefile.ErrChanged
	}
	return root.Remove(name)
}
func validateSnippetFilename(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("snippet filename must be a single nonempty filename")
	}
	return nil
}
func createSnippetDraft(app *App, name string) (string, error) {
	directory := filepath.Join(app.Cfg.StateDir(), "snippets", "drafts")
	if err := privatefile.EnsureDir(directory); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(directory, "draft-*-"+name)
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err = privatefile.ProtectCreatedFile(file); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	return path, nil
}
func writeSnippetJSON(out io.Writer, value any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
func snippetDisplay(value string) string {
	return strings.Join(strings.FieldsFunc(value, unicode.IsControl), " ")
}

// snippetCLIProcess keeps dashboard editor/publication interaction in the same CLI.
func snippetCLIProcess(app *App, args ...string) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	commandArgs := []string{}
	if app.configPath != "" {
		commandArgs = append(commandArgs, "--config", app.configPath)
	}
	commandArgs = append(commandArgs, "snippet")
	commandArgs = append(commandArgs, args...)
	return exec.Command(executable, commandArgs...), nil
}

package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/snippet"
	"github.com/daviddwlee84/dev-cli/internal/testutil"
)

func snippetFixtureProvider(kind snippet.Kind, runner SnippetRunner) *snippetProvider {
	return &snippetProvider{kind: kind, host: string(kind) + ".example.com", runner: runner, available: func(string) bool { return true }}
}
func snippetOK(body string) SnippetCommandResult {
	return SnippetCommandResult{Stdout: []byte(body), Started: true}
}
func snippetGistJSON(id string) string {
	return fmt.Sprintf(`{"id":%q,"description":"code helper","owner":{"login":"alice"},"public":false,"html_url":"https://gist.github.example.com/alice/%s","updated_at":"2026-09-22T00:00:00Z","files":{"code.py":{"filename":"code.py","size":5}}}`, id, id)
}
func snippetGitLabJSON(id int, projectID int, project string) string {
	prefix := "/-/snippets/"
	if project != "" {
		prefix = "/" + project + "/-/snippets/"
	}
	return fmt.Sprintf(`{"id":%d,"project_id":%d,"title":"code helper","author":{"username":"alice"},"visibility":"private","web_url":"https://gitlab.example.com%s%d","updated_at":"2026-09-22T00:00:00Z","files":[{"path":"code.py","raw_url":"https://gitlab.example.com%s%d/raw/main/code.py"}]}`, id, projectID, prefix, id, prefix, id)
}

func TestSnippetGitHubListsAuthenticatedOwnGistsWithPagination(t *testing.T) {
	var commands []SnippetCommand
	p := snippetFixtureProvider(snippet.GitHub, func(_ context.Context, c SnippetCommand) (SnippetCommandResult, error) {
		commands = append(commands, c)
		if c.Args[1] == "user" {
			return snippetOK(`{"login":"alice"}`), nil
		}
		if c.Args[1] != "gists" {
			t.Fatalf("unexpected endpoint: %v", c.Args)
		}
		if slices.Contains(c.Args, "page=1") {
			items := make([]string, 100)
			for i := range items {
				items[i] = snippetGistJSON(fmt.Sprintf("%032x", i+1))
			}
			return snippetOK("[" + strings.Join(items, ",") + "]"), nil
		}
		return snippetOK("[" + snippetGistJSON("ffff") + "]"), nil
	})
	result, err := p.List(t.Context(), "")
	if err != nil || !result.Complete || len(result.Items) != 101 || len(commands) != 3 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, len(commands))
	}
	for _, c := range commands {
		if !slices.Contains(c.Args, "--hostname") || !slices.Contains(c.Args, p.host) || c.Stdin != nil {
			t.Fatalf("unbound inventory command %+v", c)
		}
	}
}

func TestSnippetGitHubNeverFallsBackToAnonymousFeed(t *testing.T) {
	calls := 0
	p := snippetFixtureProvider(snippet.GitHub, func(_ context.Context, c SnippetCommand) (SnippetCommandResult, error) {
		calls++
		if c.Args[1] != "user" {
			t.Fatal("queried gist feed after failed auth")
		}
		return SnippetCommandResult{Started: true, Stderr: []byte("HTTP 401: Bad credentials")}, errors.New("exit 1")
	})
	result, err := p.List(t.Context(), "")
	if err == nil || !IsAuth(err) || result.Complete || calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
}

func TestSnippetEnterprisePathGistListAndCreateReceipt(t *testing.T) {
	body := strings.Replace(snippetGistJSON("abc123"), "https://gist.github.example.com/alice/abc123", "https://github.example.com/gist/alice/abc123", 1)
	posts := 0
	p := snippetFixtureProvider(snippet.GitHub, func(_ context.Context, request SnippetCommand) (SnippetCommandResult, error) {
		if request.Args[1] == "user" {
			return snippetOK(`{"login":"alice"}`), nil
		}
		if slices.Contains(request.Args, "POST") {
			posts++
			return snippetOK("HTTP/2.0 201 Created\r\n\r\n" + body), nil
		}
		return snippetOK("[" + body + "]"), nil
	})
	listed, err := p.List(t.Context(), "")
	if err != nil || !listed.Complete || len(listed.Items) != 1 || listed.Items[0].ID != "abc123" {
		t.Fatalf("enterprise list=%+v err=%v", listed, err)
	}
	created, err := p.Create(t.Context(), snippet.CreateRequest{Forge: snippet.GitHub, Files: []snippet.InputFile{{Name: "code.py", Content: []byte("text")}}})
	if err != nil || created.Outcome != snippet.Created || created.Item.URL != "https://github.example.com/gist/alice/abc123" || posts != 1 {
		t.Fatalf("enterprise receipt=%+v err=%v posts=%d", created, err, posts)
	}
}

func TestSnippetGitLabOwnInventoryPreservesProjectScope(t *testing.T) {
	p := snippetFixtureProvider(snippet.GitLab, func(_ context.Context, c SnippetCommand) (SnippetCommandResult, error) {
		if c.Args[1] != "snippets" {
			t.Fatalf("endpoint %v", c.Args)
		}
		return snippetOK("[" + snippetGitLabJSON(1, 0, "") + "," + snippetGitLabJSON(2, 42, "group/project") + "]"), nil
	})
	result, err := p.List(t.Context(), "")
	if err != nil || !result.Complete || len(result.Items) != 2 || result.Items[1].ProjectID != 42 || result.Items[1].Project != "group/project" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSnippetGitLabExplicitProjectRequestDoesNotTraverseInventory(t *testing.T) {
	calls := 0
	p := snippetFixtureProvider(snippet.GitLab, func(_ context.Context, c SnippetCommand) (SnippetCommandResult, error) {
		calls++
		if c.Args[1] != "projects/group%2Fproject/snippets" {
			t.Fatalf("endpoint %v", c.Args)
		}
		return snippetOK("[" + snippetGitLabJSON(2, 42, "group/project") + "]"), nil
	})
	result, err := p.List(t.Context(), "group/project")
	if err != nil || !result.Complete || calls != 1 || result.Items[0].Project != "group/project" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSnippetInventoryPreservesEarlierPagesOnFailure(t *testing.T) {
	p := snippetFixtureProvider(snippet.GitLab, func(_ context.Context, c SnippetCommand) (SnippetCommandResult, error) {
		if slices.Contains(c.Args, "page=1") {
			rows := make([]string, 100)
			for i := range rows {
				rows[i] = snippetGitLabJSON(i+1, 0, "")
			}
			return snippetOK("[" + strings.Join(rows, ",") + "]"), nil
		}
		return SnippetCommandResult{Started: true}, errors.New("timeout")
	})
	result, err := p.List(t.Context(), "")
	if err == nil || result.Complete || len(result.Items) != 100 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSnippetMissingGitLabFilesRemainUnknown(t *testing.T) {
	p := snippetFixtureProvider(snippet.GitLab, nil)
	body := strings.Replace(snippetGitLabJSON(1, 0, ""), `"files":[{"path":"code.py","raw_url":"https://gitlab.example.com/-/snippets/1/raw/main/code.py"}]`, `"file_name":"code.py"`, 1)
	item, err := p.parse([]byte(body))
	if err != nil || item.FilesComplete || len(item.Files) != 1 || item.Files[0].Ref != "" {
		t.Fatalf("item=%+v err=%v", item, err)
	}
	if _, err := p.ReadContent(t.Context(), item, item.Files[0], 100); err == nil {
		t.Fatal("used first-file raw endpoint as full coverage")
	}
}

func TestSnippetContentGitLabUsesEveryExactAPIFileRoute(t *testing.T) {
	var endpoint string
	p := snippetFixtureProvider(snippet.GitLab, func(_ context.Context, c SnippetCommand) (SnippetCommandResult, error) {
		endpoint = c.Args[1]
		return snippetOK("needle\n"), nil
	})
	item := snippet.Item{Identity: snippet.Identity{Forge: snippet.GitLab, Host: p.host, ID: "2", ProjectID: 42}}
	file := snippet.File{Name: "src/code.py", Ref: gitLabSnippetRef(p.host, "2", "src/code.py", "https://gitlab.example.com/group/project/-/snippets/2/raw/feature/topic/src/code.py")}
	content, err := p.ReadContent(t.Context(), item, file, 100)
	if err != nil || !content.Complete || string(content.Bytes) != "needle\n" || endpoint != "projects/42/snippets/2/files/feature%2Ftopic/src%2Fcode.py/raw" {
		t.Fatalf("content=%+v err=%v endpoint=%s ref=%s", content, err, endpoint, file.Ref)
	}
	for _, raw := range []string{"https://attacker.test/-/snippets/2/raw/main/src/code.py", "https://gitlab.example.com/-/snippets/3/raw/main/src/code.py", "https://gitlab.example.com/-/snippets/2/raw/../src/code.py"} {
		if ref := gitLabSnippetRef(p.host, "2", "src/code.py", raw); ref != "" {
			t.Fatalf("untrusted ref %q from %s", ref, raw)
		}
	}
}

func TestSnippetGitLabRejectsProjectPlaceholdersBeforeProviderCalls(t *testing.T) {
	p := snippetFixtureProvider(snippet.GitLab, func(context.Context, SnippetCommand) (SnippetCommandResult, error) {
		t.Fatal("native CLI received an endpoint placeholder")
		return SnippetCommandResult{}, nil
	})
	for _, project := range []string{":id", ":fullpath", "group/:repo", "group/:username"} {
		if _, err := p.List(t.Context(), project); err == nil {
			t.Fatalf("listed placeholder project %q", project)
		}
		if _, err := p.Get(t.Context(), snippet.Identity{Forge: snippet.GitLab, Host: p.host, ID: "1", Project: project}); err == nil {
			t.Fatalf("resolved placeholder project %q", project)
		}
		result, err := p.Create(t.Context(), snippet.CreateRequest{Forge: snippet.GitLab, Project: project, Files: []snippet.InputFile{{Name: "file.txt", Content: []byte("text")}}})
		if err == nil || result.Outcome != snippet.NotCreated {
			t.Fatalf("published placeholder project %q: %+v %v", project, result, err)
		}
	}
}

func TestSnippetGitLabRawFileEscapesNativePlaceholders(t *testing.T) {
	var endpoint string
	p := snippetFixtureProvider(snippet.GitLab, func(_ context.Context, request SnippetCommand) (SnippetCommandResult, error) {
		endpoint = request.Args[1]
		return snippetOK("literal filename content"), nil
	})
	item := snippet.Item{Identity: snippet.Identity{Forge: snippet.GitLab, Host: p.host, ID: "1"}}
	file := snippet.File{Name: "notes/:username.txt", Ref: ":branch"}
	content, err := p.ReadContent(t.Context(), item, file, 100)
	if err != nil || !content.Complete || endpoint != "snippets/1/files/%3Abranch/notes%2F%3Ausername.txt/raw" {
		t.Fatalf("native endpoint was not literal: %q; content=%+v err=%v", endpoint, content, err)
	}
	for _, placeholder := range []string{":branch", ":fullpath", ":group", ":id", ":namespace", ":repo", ":user", ":username"} {
		if strings.Contains(snippetAPISegment("file"+placeholder), ":") {
			t.Fatalf("unencoded native placeholder %s", placeholder)
		}
	}
}

func TestSnippetContentGitHubPinsRevisionAndReportsTruncation(t *testing.T) {
	var command SnippetCommand
	p := snippetFixtureProvider(snippet.GitHub, func(_ context.Context, c SnippetCommand) (SnippetCommandResult, error) {
		command = c
		return snippetOK(`{"content":"needle","truncated":true,"size":2000000}`), nil
	})
	item := snippet.Item{Identity: snippet.Identity{Forge: snippet.GitHub, Host: p.host, ID: "abc"}, Revision: strings.Repeat("a", 40)}
	content, err := p.ReadContent(t.Context(), item, snippet.File{Name: `code".py`}, 100)
	if err != nil || content.Complete || string(content.Bytes) != "needle" || command.Args[1] != "gists/abc/"+item.Revision {
		t.Fatalf("content=%+v err=%v command=%+v", content, err, command)
	}
	if !strings.Contains(strings.Join(command.Args, " "), `.files["code\".py"]`) {
		t.Fatalf("unsafe filename projection: %v", command.Args)
	}
}

func TestSnippetGetRejectsDifferentProviderIDAndProject(t *testing.T) {
	p := snippetFixtureProvider(snippet.GitLab, func(context.Context, SnippetCommand) (SnippetCommandResult, error) {
		return snippetOK(snippetGitLabJSON(2, 42, "group/project")), nil
	})
	for _, id := range []snippet.Identity{
		{Forge: snippet.GitHub, Host: p.host, ID: "2"},
		{Forge: snippet.GitLab, Host: "attacker.test", ID: "2"},
		{Forge: snippet.GitLab, Host: p.host, ID: "3"},
		{Forge: snippet.GitLab, Host: p.host, ID: "2", Project: "other/project"},
		{Forge: snippet.GitLab, Host: p.host, ID: "2", ProjectID: 43},
	} {
		if item, err := p.Get(t.Context(), id); err == nil {
			t.Fatalf("accepted mismatched identity %+v => %+v", id, item)
		}
	}
	for _, project := range []string{"42", "group/project"} {
		if _, err := p.Get(t.Context(), snippet.Identity{Forge: snippet.GitLab, Host: p.host, ID: "2", Project: project}); err != nil {
			t.Fatalf("valid project %s: %v", project, err)
		}
	}
}

func TestSnippetCreateUsesFrozenJSONStdinAndExactProject(t *testing.T) {
	for _, kind := range []snippet.Kind{snippet.GitHub, snippet.GitLab} {
		t.Run(string(kind), func(t *testing.T) {
			var posted SnippetCommand
			posts := 0
			p := snippetFixtureProvider(kind, func(_ context.Context, c SnippetCommand) (SnippetCommandResult, error) {
				if c.Args[1] == "user" {
					return snippetOK(`{"login":"alice","username":"alice"}`), nil
				}
				posts++
				posted = c
				body := snippetGistJSON("abc")
				if kind == snippet.GitLab {
					body = snippetGitLabJSON(1, 42, "group/project")
				}
				return snippetOK("HTTP/2.0 201 Created\r\nContent-Type: application/json\r\n\r\n" + body), nil
			})
			r := snippet.CreateRequest{Forge: kind, ExpectedOwner: "alice", Files: []snippet.InputFile{{Name: "code.py", Content: []byte("TOP_SECRET_TEST_TEXT\n")}}}
			if kind == snippet.GitLab {
				r.Project = "group/project"
			}
			result, err := p.Create(t.Context(), r)
			if err != nil || result.Outcome != snippet.Created || posts != 1 {
				t.Fatalf("result=%+v err=%v posts=%d", result, err, posts)
			}
			if strings.Contains(strings.Join(posted.Args, " "), "TOP_SECRET_TEST_TEXT") || !slices.Contains(posted.Args, "--input") || !slices.Contains(posted.Args, "--include") {
				t.Fatalf("unsafe publication transport: %+v", posted.Args)
			}
			if header := slices.Index(posted.Args, "--header"); header < 0 || header+1 >= len(posted.Args) || posted.Args[header+1] != "Content-Type: application/json" {
				t.Fatalf("JSON stdin lacks its content type: %v", posted.Args)
			}
			var payload map[string]any
			if json.Unmarshal(posted.Stdin, &payload) != nil {
				t.Fatalf("invalid stdin JSON: %s", posted.Stdin)
			}
			if kind == snippet.GitLab && posted.Args[1] != "projects/group%2Fproject/snippets" {
				t.Fatalf("project endpoint=%s", posted.Args[1])
			}
			if kind == snippet.GitHub && payload["public"] != false {
				t.Fatal("GitHub default was not secret")
			}
			if kind == snippet.GitLab && payload["visibility"] != "private" {
				t.Fatal("GitLab default was not private")
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "TOP_SECRET_TEST_TEXT") {
				t.Fatal("input leaked into result")
			}
		})
	}
}

func TestSnippetCreateRefusesChangedAccountBeforePOST(t *testing.T) {
	posts := 0
	p := snippetFixtureProvider(snippet.GitHub, func(_ context.Context, c SnippetCommand) (SnippetCommandResult, error) {
		if c.Args[1] != "user" {
			posts++
		}
		return snippetOK(`{"login":"bob"}`), nil
	})
	result, err := p.Create(t.Context(), snippet.CreateRequest{Forge: snippet.GitHub, ExpectedOwner: "alice", Files: []snippet.InputFile{{Name: "code.py", Content: []byte("text")}}})
	if err == nil || result.Outcome != snippet.NotCreated || posts != 0 {
		t.Fatalf("result=%+v err=%v posts=%d", result, err, posts)
	}
}

func TestSnippetCreateUnknownNeverRetriesAndDoesNotExposeBody(t *testing.T) {
	for _, scenario := range []struct {
		name, body string
		started    bool
		want       snippet.CreateOutcome
	}{
		{"timeout", "", true, snippet.Unknown},
		{"malformed receipt", "HTTP/2.0 201 Created\n\n{", true, snippet.Unknown},
		{"server error", "HTTP/2.0 500 Failure\n\nTOP_SECRET_TEST_TEXT", true, snippet.Unknown},
		{"rejected", "HTTP/2.0 422 Invalid\n\nTOP_SECRET_TEST_TEXT", true, snippet.NotCreated},
		{"not started", "", false, snippet.NotCreated},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			posts := 0
			p := snippetFixtureProvider(snippet.GitHub, func(_ context.Context, c SnippetCommand) (SnippetCommandResult, error) {
				if c.Args[1] == "user" {
					return snippetOK(`{"login":"alice"}`), nil
				}
				posts++
				return SnippetCommandResult{Started: scenario.started, Stdout: []byte(scenario.body), Stderr: []byte("TOP_SECRET_TEST_TEXT")}, errors.New("TOP_SECRET_TEST_TEXT")
			})
			result, err := p.Create(t.Context(), snippet.CreateRequest{Forge: snippet.GitHub, Files: []snippet.InputFile{{Name: "code.py", Content: []byte("TOP_SECRET_TEST_TEXT")}}})
			if err == nil || result.Outcome != scenario.want || posts != 1 || strings.Contains(err.Error(), "TOP_SECRET_TEST_TEXT") {
				t.Fatalf("result=%+v err=%v posts=%d", result, err, posts)
			}
		})
	}
}

func TestSnippetCreateRetainsVerifiedReceiptAfterPostProcessError(t *testing.T) {
	p := snippetFixtureProvider(snippet.GitHub, func(_ context.Context, c SnippetCommand) (SnippetCommandResult, error) {
		if c.Args[1] == "user" {
			return snippetOK(`{"login":"alice"}`), nil
		}
		return SnippetCommandResult{Started: true, Overflow: true, Stdout: []byte("HTTP/2.0 201 Created\n\n" + snippetGistJSON("abc"))}, errors.New("process failed after response")
	})
	result, err := p.Create(t.Context(), snippet.CreateRequest{Forge: snippet.GitHub, Files: []snippet.InputFile{{Name: "code.py", Content: []byte("text")}}})
	if err != nil || result.Outcome != snippet.Created || result.Item.ID != "abc" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSnippetCreateRetainsUnexpectedPublicationEffects(t *testing.T) {
	for _, scenario := range []string{"owner", "visibility", "project"} {
		t.Run(scenario, func(t *testing.T) {
			body := snippetGitLabJSON(1, 42, "group/project")
			request := snippet.CreateRequest{Forge: snippet.GitLab, Project: "group/project", Files: []snippet.InputFile{{Name: "code.py", Content: []byte("text")}}}
			switch scenario {
			case "owner":
				body = strings.Replace(body, `"username":"alice"`, `"username":"bob"`, 1)
			case "visibility":
				body = strings.Replace(body, `"visibility":"private"`, `"visibility":"public"`, 1)
			case "project":
				request.Project = "other/project"
			}
			p := snippetFixtureProvider(snippet.GitLab, func(_ context.Context, c SnippetCommand) (SnippetCommandResult, error) {
				if c.Args[1] == "user" {
					return snippetOK(`{"username":"alice"}`), nil
				}
				return snippetOK("HTTP/2.0 201 Created\n\n" + body), nil
			})
			result, err := p.Create(t.Context(), request)
			if err == nil || result.Outcome != snippet.Created || result.Item.ID != "1" || result.Item.Project != "group/project" {
				t.Fatalf("lost or disguised created result: %+v %v", result, err)
			}
		})
	}
}

func TestSnippetCreateRejectsEndpointDriftWithoutAnyProviderCall(t *testing.T) {
	p := snippetFixtureProvider(snippet.GitHub, func(context.Context, SnippetCommand) (SnippetCommandResult, error) {
		t.Fatal("endpoint drift called provider")
		return SnippetCommandResult{}, nil
	})
	result, err := p.Create(t.Context(), snippet.CreateRequest{Forge: snippet.GitHub, Host: "other.example.com", Files: []snippet.InputFile{{Name: "code.py", Content: []byte("text")}}})
	if err == nil || result.Outcome != snippet.NotCreated {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSnippetNativeTransportCarriesBodyOnlyOnStdin(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "request.json")
	testutil.GoCommand(t, dir, "gh", `package main
import("encoding/json";"io";"os";"fmt";"strings")
func main(){
 if len(os.Args)>2&&os.Args[2]=="user"{fmt.Print("{\"login\":\"alice\"}");return}
 data,_:=io.ReadAll(os.Stdin)
 body,_:=json.Marshal(struct{Args []string;Stdin string}{os.Args[1:],string(data)})
 if err:=os.WriteFile(os.Getenv("SNIPPET_FIXTURE_LOG"),body,0600);err!=nil{panic(err)}
 if strings.Contains(strings.Join(os.Args[1:]," "),"TOP_SECRET_TEST_TEXT"){os.Exit(97)}
 fmt.Print("HTTP/2.0 201 Created\r\nContent-Type: application/json\r\n\r\n")
 fmt.Print(`+"`"+snippetGistJSON("abc")+"`"+`)
}
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SNIPPET_FIXTURE_LOG", log)
	p := snippetFixtureProvider(snippet.GitHub, runSnippetCommand)
	result, err := p.Create(t.Context(), snippet.CreateRequest{Forge: snippet.GitHub, Files: []snippet.InputFile{{Name: "code.py", Content: []byte("TOP_SECRET_TEST_TEXT\n")}}})
	if err != nil || result.Outcome != snippet.Created {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Args  []string
		Stdin string
	}
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(request.Stdin, "TOP_SECRET_TEST_TEXT") || strings.Contains(strings.Join(request.Args, " "), "TOP_SECRET_TEST_TEXT") {
		t.Fatalf("transport leaked or omitted body: %s", data)
	}
}

func TestSnippetNativeTransportBoundsSubprocessOutput(t *testing.T) {
	dir := t.TempDir()
	bin := testutil.GoCommand(t, dir, "large-snippet", `package main
import("fmt";"strings")
func main(){fmt.Print(strings.Repeat("x",1000000))}
`)
	result, err := runSnippetCommand(t.Context(), SnippetCommand{Bin: bin, MaxOutputBytes: 32})
	if err == nil || !result.Started || !result.Overflow || len(result.Stdout) != 32 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

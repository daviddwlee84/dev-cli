// Package help serves the git-workflow quick-reference pages dev bundles.
//
// These exist because the hard part of this workflow is not running the
// commands, it is remembering which one applies. A page you can reach in one
// keystroke from the terminal you are already in beats a bookmark.
package help

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed all:topics
var topics embed.FS

// Topic is one reference page.
type Topic struct {
	Name    string
	Title   string
	Summary string
	Body    string
}

// List returns every topic, sorted by name.
func List() ([]Topic, error) {
	entries, err := fs.ReadDir(topics, "topics")
	if err != nil {
		return nil, err
	}
	var out []Topic
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		t, err := load(strings.TrimSuffix(e.Name(), ".md"))
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one topic by exact name, or by unique prefix.
func Get(name string) (Topic, error) {
	if t, err := load(name); err == nil {
		return t, nil
	}
	all, err := List()
	if err != nil {
		return Topic{}, err
	}
	var hits []Topic
	for _, t := range all {
		if strings.HasPrefix(t.Name, name) || strings.Contains(t.Name, name) {
			hits = append(hits, t)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return Topic{}, fmt.Errorf("no help topic %q; run `dev help` to list them", name)
	default:
		names := make([]string, len(hits))
		for i, t := range hits {
			names[i] = t.Name
		}
		return Topic{}, fmt.Errorf("%q matches several topics: %s", name, strings.Join(names, ", "))
	}
}

func load(name string) (Topic, error) {
	b, err := topics.ReadFile("topics/" + name + ".md")
	if err != nil {
		return Topic{}, err
	}
	body := string(b)
	t := Topic{Name: name, Body: body}
	// The first heading is the title; the first paragraph after it is the
	// summary shown in the index.
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case t.Title == "" && strings.HasPrefix(line, "# "):
			t.Title = strings.TrimPrefix(line, "# ")
		case t.Title != "" && t.Summary == "" && line != "" && !strings.HasPrefix(line, "#"):
			// A page may open with a blockquote or a list; the marker is
			// formatting, not part of the one-line summary.
			t.Summary = strings.TrimLeft(line, ">-* ")
			return t, nil
		}
	}
	return t, nil
}

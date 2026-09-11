package help

import (
	"sort"
	"strings"
)

// SearchHit points at the original Markdown line, independent of display width.
type SearchHit struct {
	Topic            Topic
	Heading, Snippet string
	Line             int
	score            int
}

func Search(query string) ([]SearchHit, error) {
	topics, err := List()
	if err != nil {
		return nil, err
	}
	return SearchTopics(topics, query), nil
}

// SearchTopics searches already loaded embedded content; it performs no I/O.
func SearchTopics(topics []Topic, query string) []SearchHit {
	terms := strings.Fields(strings.ToLower(query))
	var hits []SearchHit
	for _, topic := range topics {
		text := strings.ToLower(topic.Name + " " + topic.Title + " " + topic.Body)
		matched := true
		for _, term := range terms {
			if !strings.Contains(text, term) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		hit := SearchHit{Topic: topic, Snippet: topic.Summary}
		for _, term := range terms {
			if strings.Contains(strings.ToLower(topic.Name), term) {
				hit.score += 8
			}
			if strings.Contains(strings.ToLower(topic.Title), term) {
				hit.score += 5
			}
		}
		heading := ""
		best := -1
		for n, line := range strings.Split(topic.Body, "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "#") {
				heading = strings.TrimSpace(strings.TrimLeft(trim, "#"))
			}
			score := 0
			for _, term := range terms {
				if strings.Contains(strings.ToLower(line), term) {
					score++
				}
			}
			if len(terms) > 0 && score > 0 && (score > best || score == best && strings.HasPrefix(hit.Snippet, "#") && !strings.HasPrefix(trim, "#")) {
				best = score
				hit.Line = n
				hit.Heading = heading
				hit.Snippet = trim
			}
		}
		hits = append(hits, hit)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].Topic.Name < hits[j].Topic.Name
	})
	return hits
}

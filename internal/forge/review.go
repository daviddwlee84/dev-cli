package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ReviewQuery identifies one exact head/base relationship in one repository.
// Repository is the forge's stable repository identity: owner/repository for
// GitHub, group/repository for GitLab, or organization/project/repository for
// Azure DevOps. Adapters must still verify the returned branches because CLI
// filters are not sufficient evidence on their own.
type ReviewQuery struct {
	Repository string `json:"repository"`
	Head       string `json:"head"`
	Base       string `json:"base"`
}

// ReviewState is the portable lifecycle state observed for a provider review.
type ReviewState string

const (
	ReviewOpen   ReviewState = "open"
	ReviewDraft  ReviewState = "draft"
	ReviewMerged ReviewState = "merged"
	ReviewClosed ReviewState = "closed"
)

// Valid reports whether s has defined portable review semantics.
func (s ReviewState) Valid() bool {
	switch s {
	case ReviewOpen, ReviewDraft, ReviewMerged, ReviewClosed:
		return true
	default:
		return false
	}
}

// Review is the minimal provider evidence for an exact head/base relationship.
// ID is the provider-stable identity and Number is the repository-local human
// identifier. Head, Base, State and Draft are observations from the provider
// response rather than values copied from ReviewQuery.
type Review struct {
	Provider   Kind        `json:"provider"`
	Repository string      `json:"repository"`
	ID         string      `json:"id"`
	Number     int         `json:"number"`
	State      ReviewState `json:"state"`
	Draft      bool        `json:"draft"`
	URL        string      `json:"url"`
	Head       string      `json:"head"`
	Base       string      `json:"base"`
	ObservedAt time.Time   `json:"observed_at"`
}

// ReviewQuerier is an optional forge capability. A nil Review and nil error is
// a successful observation that no exact review exists. One exact match returns
// provider evidence; multiple exact matches fail with AmbiguousReviewError.
type ReviewQuerier interface {
	QueryReview(ctx context.Context, dir string, query ReviewQuery) (*Review, error)
}

var (
	// ErrAmbiguousReview identifies a query with multiple exact head/base
	// matches. Callers must not select one by recency or provider ordering.
	ErrAmbiguousReview = errors.New("multiple reviews match")
	// ErrMalformedReviewResponse identifies provider JSON that cannot supply the
	// critical portable fields without guessing.
	ErrMalformedReviewResponse = errors.New("malformed review response")
)

// AmbiguousReviewError carries every exact match while keeping Error text free
// of provider response values such as private URLs and branch names.
type AmbiguousReviewError struct {
	Provider Kind
	Query    ReviewQuery
	Matches  []Review
}

func (e *AmbiguousReviewError) Error() string {
	return fmt.Sprintf("%s review query returned %d exact matches", e.Provider, len(e.Matches))
}

func (e *AmbiguousReviewError) Unwrap() error { return ErrAmbiguousReview }

// MalformedReviewResponseError identifies the unusable response field without
// embedding its value or the response body. Index is zero-based and is -1 when
// the whole JSON document is malformed. Err retains safe decoder diagnostics.
type MalformedReviewResponseError struct {
	Provider Kind
	Index    int
	Field    string
	Err      error
}

func (e *MalformedReviewResponseError) Error() string {
	location := "response"
	if e.Index >= 0 {
		location = fmt.Sprintf("response item %d", e.Index+1)
	}
	detail := "missing or invalid " + e.Field
	if e.Err != nil {
		detail = e.Err.Error()
	}
	return fmt.Sprintf("%s review %s is malformed: %s", e.Provider, location, detail)
}

func (e *MalformedReviewResponseError) Unwrap() error { return e.Err }

func (e *MalformedReviewResponseError) Is(target error) bool {
	return target == ErrMalformedReviewResponse
}

// QueryReview invokes the optional capability without widening Forge. It never
// caches or initiates a query unless a caller explicitly calls this function.
func QueryReview(ctx context.Context, f Forge, dir string, query ReviewQuery) (*Review, error) {
	querier, ok := f.(ReviewQuerier)
	if !ok {
		return nil, &ErrUnsupported{Kind: f.Kind(), Operation: "review queries"}
	}
	return querier.QueryReview(ctx, dir, query)
}

type reviewCandidate struct {
	ID                      string
	Number                  int
	State                   string
	Draft                   *bool
	URL                     string
	Head                    string
	Base                    string
	SourceMatchesRepository *bool
}

type reviewStateNormalizer func(state string, draft bool) (ReviewState, error)

func validateReviewQuery(query ReviewQuery) error {
	switch {
	case strings.TrimSpace(query.Repository) == "":
		return errors.New("review query repository is required")
	case strings.TrimSpace(query.Head) == "":
		return errors.New("review query head is required")
	case strings.TrimSpace(query.Base) == "":
		return errors.New("review query base is required")
	default:
		return nil
	}
}

func decodeReviewJSON(provider Kind, out string, target any) error {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || trimmed == "null" {
		return malformedReviewResponse(provider, -1, "JSON array", nil)
	}
	if err := json.Unmarshal([]byte(trimmed), target); err != nil {
		return malformedReviewResponse(provider, -1, "JSON", err)
	}
	return nil
}

func selectExactReview(provider Kind, query ReviewQuery, candidates []reviewCandidate, normalize reviewStateNormalizer) (*Review, error) {
	matches := make([]Review, 0, 1)
	observedAt := time.Now().UTC()
	for index, candidate := range candidates {
		if candidate.Head == "" {
			return nil, malformedReviewResponse(provider, index, "head branch", nil)
		}
		if candidate.Base == "" {
			return nil, malformedReviewResponse(provider, index, "base branch", nil)
		}
		if candidate.Head != query.Head || candidate.Base != query.Base {
			continue
		}
		if candidate.SourceMatchesRepository == nil {
			return nil, malformedReviewResponse(provider, index, "source repository identity", nil)
		}
		if !*candidate.SourceMatchesRepository {
			continue
		}
		if strings.TrimSpace(candidate.ID) == "" {
			return nil, malformedReviewResponse(provider, index, "identifier", nil)
		}
		if candidate.Number <= 0 {
			return nil, malformedReviewResponse(provider, index, "number", nil)
		}
		if candidate.Draft == nil {
			return nil, malformedReviewResponse(provider, index, "draft flag", nil)
		}
		if !validReviewURL(candidate.URL) {
			return nil, malformedReviewResponse(provider, index, "URL", nil)
		}
		state, err := normalize(candidate.State, *candidate.Draft)
		if err != nil {
			return nil, malformedReviewResponse(provider, index, "state", err)
		}
		matches = append(matches, Review{
			Provider: provider, Repository: query.Repository,
			ID: candidate.ID, Number: candidate.Number,
			State: state, Draft: *candidate.Draft, URL: candidate.URL,
			Head: candidate.Head, Base: candidate.Base, ObservedAt: observedAt,
		})
	}

	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &matches[0], nil
	default:
		return nil, &AmbiguousReviewError{
			Provider: provider, Query: query,
			Matches: append([]Review(nil), matches...),
		}
	}
}

func malformedReviewResponse(provider Kind, index int, field string, err error) error {
	return &MalformedReviewResponseError{Provider: provider, Index: index, Field: field, Err: err}
}

func validReviewURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "https" || parsed.Scheme == "http"
}

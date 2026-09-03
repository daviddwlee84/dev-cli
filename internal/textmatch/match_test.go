package textmatch

import "testing"

func TestTerms(t *testing.T) {
	for _, tc := range []struct {
		name     string
		haystack string
		query    string
		want     bool
	}{
		{name: "empty", haystack: "anything", want: true},
		{name: "case insensitive", haystack: "API Token Auth", query: "api AUTH", want: true},
		{name: "terms out of order", haystack: "api token auth", query: "auth api", want: true},
		{name: "missing term", haystack: "api token", query: "api auth", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Terms(tc.haystack, tc.query); got != tc.want {
				t.Fatalf("Terms(%q, %q) = %t, want %t", tc.haystack, tc.query, got, tc.want)
			}
		})
	}
}

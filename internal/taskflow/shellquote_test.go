package taskflow

import "testing"

func TestShellQuoteQuotesBacktickCommandSubstitution(t *testing.T) {
	got := shellQuote("feat/`touch-pwned`")
	if got != "'feat/`touch-pwned`'" {
		t.Fatalf("shellQuote(backticks) = %q", got)
	}
	if plain := shellQuote("feat/safe-name"); plain != "feat/safe-name" {
		t.Fatalf("shellQuote(safe) = %q", plain)
	}
}

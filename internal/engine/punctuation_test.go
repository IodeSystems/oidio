package engine

import "testing"

func TestApplySpokenPunctuation(t *testing.T) {
	cases := []struct{ in, want string }{
		// attaches to the preceding word (no space before the symbol)
		{"are you sure question mark", "are you sure?"},
		{"that is amazing exclamation point", "that is amazing!"},
		{"exclamation mark", "!"},                 // "mark" synonym
		{"QUESTION MARK", "?"},                     // upper-case transducer output
		{"WHAT QUESTION MARK THAT IS WILD EXCLAMATION POINT", "WHAT? THAT IS WILD!"},
		{"no punctuation here", "no punctuation here"}, // untouched
		{"a question about marks", "a question about marks"}, // no false positive
	}
	for _, c := range cases {
		if got := applySpokenPunctuation(c.in); got != c.want {
			t.Errorf("applySpokenPunctuation(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

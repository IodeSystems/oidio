package engine

import "regexp"

// Spoken-punctuation post-processing: dictated punctuation words → symbols,
// attaching to the preceding word (the leading whitespace is consumed) so
// "are you sure question mark" → "are you sure?". Case-insensitive, so it works
// on the streaming transducer's upper-case output ("QUESTION MARK") as well as
// whisper's cased output. Opt-in per model (ModelSpec.SpokenPunctuation); off by
// default, since raw transcription shouldn't silently rewrite these words.
var spokenPunctuation = []struct {
	re  *regexp.Regexp
	sym string
}{
	{regexp.MustCompile(`(?i)\s*\bquestion mark\b`), "?"},
	{regexp.MustCompile(`(?i)\s*\bexclamation (?:point|mark)\b`), "!"},
}

// applySpokenPunctuation rewrites dictated punctuation words to their symbols.
func applySpokenPunctuation(s string) string {
	for _, p := range spokenPunctuation {
		s = p.re.ReplaceAllString(s, p.sym)
	}
	return s
}

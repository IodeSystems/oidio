package engine

import "testing"

func spk(ids ...int) []TimedWord {
	w := make([]TimedWord, len(ids))
	for i, s := range ids {
		w[i] = TimedWord{Text: "w", Start: float64(i) * 0.3, Speaker: s}
	}
	return w
}

func ids(w []TimedWord) []int {
	out := make([]int, len(w))
	for i := range w {
		out[i] = w[i].Speaker
	}
	return out
}

func eq(t *testing.T, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

// A one-word flip between two speakers is a span boundary landing mid-word, not
// someone saying a single word and falling silent.
func TestSmoothAbsorbsSingleWordBlip(t *testing.T) {
	w := spk(0, 0, 1, 0, 0)
	smoothRuns(w)
	eq(t, ids(w), []int{0, 0, 0, 0, 0})
}

// A real turn exchange leaves the floor with someone else, so the flanks differ
// — that must survive untouched however short it is.
func TestSmoothKeepsShortRunWithDifferentFlanks(t *testing.T) {
	w := spk(0, 0, 1, 2, 2)
	smoothRuns(w)
	eq(t, ids(w), []int{0, 0, 1, 2, 2})
}

// A longer run is diarization asserting someone spoke. Absorbing it would hide a
// real error behind a tidier transcript.
func TestSmoothKeepsRunsAboveBlipLength(t *testing.T) {
	w := spk(0, 0, 1, 1, 1, 0, 0)
	smoothRuns(w)
	eq(t, ids(w), []int{0, 0, 1, 1, 1, 0, 0})
}

// A blip at either end has only one flank, so there is no evidence it was a
// wobble rather than the recording opening or closing on that speaker.
func TestSmoothLeavesEdgeRunsAlone(t *testing.T) {
	w := spk(1, 0, 0, 0)
	smoothRuns(w)
	eq(t, ids(w), []int{1, 0, 0, 0})
}

// Words go to whoever was speaking; where spans overlap, to the one the word sits
// deepest inside. A word in no span at all takes the nearest rather than vanishing.
func TestAssignSpeakersByTime(t *testing.T) {
	spans := []span{{0, 10, 7}, {10, 20, 3}}
	w := []TimedWord{{Start: 1}, {Start: 9.9}, {Start: 15}, {Start: 99}, {Start: -5}}
	assignSpeakers(w, spans)
	eq(t, ids(w), []int{7, 7, 3, 3, 7})
}

func TestAssignSpeakersPrefersTheSpanTheWordSitsDeepestInside(t *testing.T) {
	// Overlapping spans: 4.9 is 0.1s inside A's tail but 4.9s inside B.
	spans := []span{{0, 5, 1}, {0.1, 20, 2}}
	w := []TimedWord{{Start: 4.9}}
	assignSpeakers(w, spans)
	eq(t, ids(w), []int{2})
}

// Sub-word tokens become whole words: a leading space starts a new one, so an
// apostrophe never lands on a different speaker than the word it belongs to.
func TestWordsFromTokensGroupsOnLeadingSpace(t *testing.T) {
	toks := []string{" you", "'", "re", " about"}
	times := []float32{0.5, 0.6, 0.6, 0.8}
	got := wordsFromTokens(toks, times, 10, 0)
	if len(got) != 2 || got[0].Text != "you're" || got[1].Text != "about" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Start != 10.5 {
		t.Fatalf("offset not applied: %v", got[0].Start)
	}
}

// The window seam: words re-read from the previous window are dropped so they are
// not transcribed twice.
func TestWordsFromTokensDropsBeforeFrom(t *testing.T) {
	toks := []string{" a", " b", " c"}
	times := []float32{0.0, 1.0, 2.0}
	got := wordsFromTokens(toks, times, 100, 101.5)
	if len(got) != 1 || got[0].Text != "c" {
		t.Fatalf("got %+v", got)
	}
}

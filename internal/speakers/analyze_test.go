package speakers

import "testing"

// vec builds a distinguishable unit-ish embedding: two voices are far apart when
// their dominant dimension differs.
func vec(dim int, n int) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = 0.1
	}
	v[dim] = 1
	return v
}

func seg(idx int, start, end float64, spk string, dim int, text string) Segment {
	return Segment{Index: idx, Start: start, End: end, Speaker: spk, Text: text, Embedding: vec(dim, 8)}
}

// The failure this exists for: one cluster holding two people. The passage that
// sounds like the OTHER speaker must be proposed for a move.
func TestContaminatedClusterProposesTheMisattributedPassage(t *testing.T) {
	segs := []Segment{
		seg(0, 0, 30, "judge", 0, "please raise your right hand"),
		seg(1, 30, 60, "judge", 0, "any questions"),
		seg(2, 60, 90, "party", 1, "my sewer has been broken"),
		seg(3, 90, 120, "party", 1, "my lawyer is handling it"),
		// Actually the judge, but diarization filed it under the party.
		seg(4, 120, 130, "party", 0, "do you swear to tell the truth"),
	}
	a := Analyze(segs, DefaultParams(), nil)
	if len(a.Moves) != 1 {
		t.Fatalf("want exactly 1 move, got %d: %+v", len(a.Moves), a.Moves)
	}
	m := a.Moves[0]
	if m.Segment != 4 || m.From != "party" || m.To != "judge" {
		t.Fatalf("wrong move: %+v", m)
	}
	if m.Acoustic <= 0.5 {
		t.Fatalf("a clear-cut move should carry real confidence, got %.2f", m.Acoustic)
	}
}

// Merging is the wrong tool for contamination. Two genuinely different voices
// must never be proposed as one, however the segments are filed.
func TestDistinctVoicesAreNotProposedForJoin(t *testing.T) {
	segs := []Segment{
		seg(0, 0, 30, "a", 0, "x"),
		seg(1, 30, 60, "b", 1, "y"),
	}
	a := Analyze(segs, DefaultParams(), nil)
	if len(a.Joins) != 0 {
		t.Fatalf("distinct voices must not be joined: %+v", a.Joins)
	}
}

// The over-splitting case: one person under two ids. That IS a join.
func TestSameVoiceUnderTwoIdsIsProposedForJoin(t *testing.T) {
	segs := []Segment{
		seg(0, 0, 30, "a", 3, "x"),
		seg(1, 30, 60, "b", 3, "y"),
	}
	a := Analyze(segs, DefaultParams(), nil)
	if len(a.Joins) != 1 {
		t.Fatalf("want 1 join, got %+v", a.Joins)
	}
	if a.Joins[0].Cos < 0.99 {
		t.Fatalf("identical voices should be near 1.0, got %.3f", a.Joins[0].Cos)
	}
}

// A near-tie is not evidence that diarization was wrong. Without a margin,
// segments oscillate between similar voices on noise.
func TestNearTieIsNotMoved(t *testing.T) {
	segs := []Segment{
		seg(0, 0, 30, "a", 0, "x"),
		seg(1, 30, 60, "b", 0, "y"), // identical voice, different id
	}
	p := DefaultParams()
	p.JoinCos = 2 // disable joins so only move behaviour is under test
	a := Analyze(segs, p, nil)
	for _, m := range a.Moves {
		if m.Margin < p.MinMargin {
			t.Fatalf("moved on a sub-margin difference: %+v", m)
		}
	}
}

// Short segments embed unreliably, so they are reported as skipped rather than
// moved on weak evidence — and reported, not silently dropped.
func TestShortSegmentsAreSkippedNotMoved(t *testing.T) {
	segs := []Segment{
		seg(0, 0, 30, "a", 0, "long"),
		seg(1, 30, 60, "b", 1, "long"),
		seg(2, 60, 60.5, "b", 0, "hm"), // 0.5s: sounds like a, too short to trust
	}
	a := Analyze(segs, DefaultParams(), nil)
	for _, m := range a.Moves {
		if m.Segment == 2 {
			t.Fatalf("moved a 0.5s segment: %+v", m)
		}
	}
	if a.Skipped != 1 {
		t.Fatalf("want 1 skipped, got %d", a.Skipped)
	}
}

// Apply must rewrite the raw document, not a parallel copy, so nothing the
// server emitted is dropped on the way through.
func TestApplyRewritesSpeakersAndFoldsJoins(t *testing.T) {
	tr := &Transcript{Raw: map[string]any{
		"text": "keep me",
		"segments": []any{
			map[string]any{"id": float64(0), "speaker": "a", "text": "one"},
			map[string]any{"id": float64(1), "speaker": "b", "text": "two"},
		},
		"speakers": []any{
			map[string]any{"uuid": "a"}, map[string]any{"uuid": "b"},
		},
	}}
	doc := tr.Apply(Analysis{Joins: []Join{{A: "a", B: "b"}}})
	segs := doc["segments"].([]any)
	for _, v := range segs {
		if got := v.(map[string]any)["speaker"]; got != "a" {
			t.Fatalf("join not applied: %v", got)
		}
	}
	if n := len(doc["speakers"].([]any)); n != 1 {
		t.Fatalf("folded speaker not removed: %d remain", n)
	}
	if doc["text"] != "keep me" {
		t.Fatalf("unrelated fields must round-trip")
	}
}

// --apply-agreed must never write a proposal the content reviewer did not back,
// including one it never saw.
func TestFilterKeepsOnlyDoublyAgreedProposals(t *testing.T) {
	a := Analysis{
		Moves: []Move{
			{Segment: 1, Semantic: &Verdict{Status: "agree"}},
			{Segment: 2, Semantic: &Verdict{Status: "disagree"}},
			{Segment: 3}, // never reviewed
		},
		Joins: []Join{{A: "x", B: "y"}},
	}
	f := Filter(a)
	if len(f.Moves) != 1 || f.Moves[0].Segment != 1 {
		t.Fatalf("wrong moves kept: %+v", f.Moves)
	}
	if len(f.Joins) != 0 {
		t.Fatalf("unreviewed join must not be applied: %+v", f.Joins)
	}
}

// mix is what an embedder actually returns for audio containing two voices: a
// blend that resembles neither cleanly. A mixed segment embedded as a PURE voice
// would match its own centroid perfectly and no split could ever be justified —
// which is exactly the state a real mixed segment is not in.
func mix(a, b, n int) []float32 {
	x, y := vec(a, n), vec(b, n)
	out := make([]float32, n)
	for i := range out {
		out[i] = (x[i] + y[i]) / 2
	}
	return out
}

// fakeSpans embeds a time range by reporting whichever synthetic voice occupies
// most of it, so split logic can be tested with no audio and no models.
func fakeSpans(bounds []struct {
	Start, End float64
	Dim        int
}) SpanEmbedder {
	// Blends proportionally to how much of the range each voice occupies, which
	// is what an embedder does. A "dominant voice wins" fake makes every candidate
	// cut equally good and hides whether the search finds the true boundary.
	return func(start, end float64) []float32 {
		out := make([]float32, 8)
		total := 0.0
		for _, b := range bounds {
			lo, hi := start, end
			if b.Start > lo {
				lo = b.Start
			}
			if b.End < hi {
				hi = b.End
			}
			if hi <= lo {
				continue
			}
			w := hi - lo
			total += w
			v := vec(b.Dim, 8)
			for i := range out {
				out[i] += v[i] * float32(w)
			}
		}
		if total == 0 {
			return nil
		}
		for i := range out {
			out[i] /= float32(total)
		}
		return out
	}
}

// The failure SPLIT exists for: diarization called one turn what was actually
// two people, so no relabelling of the whole segment can be right.
func TestSplitProposedWhenOneTurnHoldsTwoSpeakers(t *testing.T) {
	segs := []Segment{
		seg(0, 0, 30, "a", 0, "a speaks"),
		seg(1, 30, 60, "b", 1, "b speaks"),
		// Filed as one turn under a, but b takes over halfway.
		{Index: 2, Start: 60, End: 70, Speaker: "a", Text: "one two three four", Embedding: mix(0, 1, 8)},
	}
	spans := fakeSpans([]struct {
		Start, End float64
		Dim        int
	}{{0, 30, 0}, {30, 60, 1}, {60, 65, 0}, {65, 70, 1}})

	a := Analyze(segs, DefaultParams(), spans)
	if len(a.Splits) != 1 {
		t.Fatalf("want 1 split, got %d: %+v", len(a.Splits), a.Splits)
	}
	s := a.Splits[0]
	if s.Segment != 2 {
		t.Fatalf("split the wrong segment: %+v", s)
	}
	if s.At < 64 || s.At > 66 {
		t.Fatalf("cut at %.1fs, want near 65", s.At)
	}
	if s.Left == s.Right {
		t.Fatalf("a split must name two different speakers: %+v", s)
	}
	if s.LeftText == "" || s.RightText == "" {
		t.Fatalf("both halves need text: %+v", s)
	}
}

// One speaker throughout must never be cut, however long the turn.
func TestNoSplitWhenOneSpeakerExplainsTheWholeTurn(t *testing.T) {
	segs := []Segment{
		seg(0, 0, 30, "a", 0, "a"),
		seg(1, 30, 60, "b", 1, "b"),
		{Index: 2, Start: 60, End: 80, Speaker: "a", Text: "all mine", Embedding: vec(0, 8)},
	}
	spans := fakeSpans([]struct {
		Start, End float64
		Dim        int
	}{{0, 30, 0}, {30, 60, 1}, {60, 80, 0}})
	a := Analyze(segs, DefaultParams(), spans)
	if len(a.Splits) != 0 {
		t.Fatalf("must not cut a single-speaker turn: %+v", a.Splits)
	}
}

// A turn too short for both halves to embed cannot be judged, so it is left
// alone rather than cut on evidence that does not exist.
func TestNoSplitBelowTwiceTheEmbeddingFloor(t *testing.T) {
	segs := []Segment{
		seg(0, 0, 30, "a", 0, "a"),
		seg(1, 30, 60, "b", 1, "b"),
		{Index: 2, Start: 60, End: 63, Speaker: "a", Text: "too short", Embedding: vec(0, 8)},
	}
	spans := fakeSpans([]struct {
		Start, End float64
		Dim        int
	}{{0, 30, 0}, {30, 60, 1}, {60, 61.5, 0}, {61.5, 63, 1}})
	a := Analyze(segs, DefaultParams(), spans)
	if len(a.Splits) != 0 {
		t.Fatalf("3s turn cannot yield two 2s halves: %+v", a.Splits)
	}
}

// Applying a split must replace one segment with two, renumber, and leave the
// rest of the document alone.
func TestApplySplitRewritesBoundaries(t *testing.T) {
	tr := &Transcript{Raw: map[string]any{
		"text": "keep me",
		"segments": []any{
			map[string]any{"id": float64(0), "start": 0.0, "end": 10.0, "speaker": "a", "text": "one two"},
			map[string]any{"id": float64(1), "start": 10.0, "end": 20.0, "speaker": "b", "text": "three"},
		},
	}}
	doc := tr.Apply(Analysis{Splits: []Split{
		{Segment: 0, At: 5, Left: "a", Right: "b", LeftText: "one", RightText: "two"},
	}})
	segs := doc["segments"].([]any)
	if len(segs) != 3 {
		t.Fatalf("want 3 segments after a split, got %d", len(segs))
	}
	l := segs[0].(map[string]any)
	r := segs[1].(map[string]any)
	if l["end"] != 5.0 || r["start"] != 5.0 {
		t.Fatalf("boundary not applied: %v / %v", l["end"], r["start"])
	}
	if l["speaker"] != "a" || r["speaker"] != "b" {
		t.Fatalf("halves not attributed: %v / %v", l["speaker"], r["speaker"])
	}
	if l["text"] != "one" || r["text"] != "two" {
		t.Fatalf("text not divided: %v / %v", l["text"], r["text"])
	}
	for i, v := range segs {
		if got := v.(map[string]any)["id"]; got != float64(i) {
			t.Fatalf("ids not renumbered: %v at %d", got, i)
		}
	}
	if doc["text"] != "keep me" {
		t.Fatalf("unrelated fields must round-trip")
	}
}

// A cut between two ids that are the same voice is incoherent: whatever the
// gain, it is not evidence of a speaker change. This was the dominant false
// positive on real audio — shorter spans embed more cleanly, so each half beats
// the whole against SOME centroid.
func TestNoSplitBetweenNearIdenticalClusters(t *testing.T) {
	// "a" and "b" hold the same voice under two ids, so any cut between them
	// must be refused however well the halves score.
	segs := []Segment{
		seg(0, 0, 30, "a", 0, "a"),
		seg(1, 30, 60, "b", 0, "b"),
		{Index: 2, Start: 60, End: 70, Speaker: "a", Text: "one two three four", Embedding: mix(0, 1, 8)},
	}
	spans := fakeSpans([]struct {
		Start, End float64
		Dim        int
	}{{0, 30, 0}, {30, 60, 0}, {60, 65, 0}, {65, 70, 0}})
	a := Analyze(segs, DefaultParams(), spans)
	for _, sp := range a.Splits {
		t.Fatalf("cut proposed between same-voice ids: %+v", sp)
	}
}

package engine

import "testing"

func total(iv [][2]float64) float64 {
	var t float64
	for _, p := range iv {
		t += p[1] - p[0]
	}
	return t
}

// The point of subtraction: a brief interjection costs only its own duration,
// not the whole turn. Discarding the turn left the dominant speaker in one
// hearing with 168s of 564s; subtracting leaves 448s.
func TestCleanPartsSubtractsRatherThanDiscards(t *testing.T) {
	spans := []span{
		{0, 10, 1},  // A speaks 10s
		{3, 3.4, 2}, // B interjects for 0.4s
	}
	got := cleanParts(spans, 0)
	if len(got) != 2 {
		t.Fatalf("got %v, want two pieces either side of the interjection", got)
	}
	if d := total(got); d < 9.5 || d > 9.7 {
		t.Errorf("kept %.2fs, want ~9.6s (10s minus the 0.4s interjection)", d)
	}
	if d := total(cleanParts(spans, 1)); d != 0 {
		t.Errorf("interjection kept %.2fs, want 0 — it is entirely inside A", d)
	}
}

// A speaker segmented finely does not overlap themselves.
func TestSameSpeakerIsNotSubtracted(t *testing.T) {
	spans := []span{{0, 5, 7}, {4, 9, 7}, {9, 12, 7}}
	for i, want := range []float64{5, 5, 3} {
		if d := total(cleanParts(spans, i)); d != want {
			t.Errorf("span %d kept %.1fs, want %.1fs", i, d, want)
		}
	}
}

// Slivers left by subtraction carry no speaker information and only add
// boundary artifacts where the pieces are concatenated.
func TestCleanPartsDropsSlivers(t *testing.T) {
	spans := []span{{0, 10, 1}, {0.1, 9.9, 2}}
	if got := cleanParts(spans, 0); len(got) != 0 {
		t.Errorf("kept slivers %v, want none (each under %.2fs)", got, minCleanPart)
	}
}

// Fully covered means nothing clean, and the caller must fall back.
func TestCleanPartsFullyCovered(t *testing.T) {
	spans := []span{{2, 5, 1}, {0, 10, 2}}
	if d := total(cleanParts(spans, 0)); d != 0 {
		t.Errorf("kept %.2fs of a fully covered span, want 0", d)
	}
}

// A speaker whose clean audio is too thin falls back to everything: a thin clean
// print describes a voice worse than a blended thick one.
func TestCleanFallbackThreshold(t *testing.T) {
	thin := []span{{0, 20, 1}, {0.05, 19.9, 2}, {30, 30.2, 1}}
	var got float64
	for i, sp := range thin {
		if sp.spk == 1 {
			got += total(cleanParts(thin, i))
		}
	}
	if got >= minCleanSpeech {
		t.Fatalf("fixture wrong: clean=%.2fs should be under the %.1fs threshold", got, minCleanSpeech)
	}

	ample := []span{{0, 20, 1}, {5, 6, 2}, {30, 40, 1}}
	got = 0
	for i, sp := range ample {
		if sp.spk == 1 {
			got += total(cleanParts(ample, i))
		}
	}
	if got < minCleanSpeech {
		t.Errorf("clean=%.1fs should clear the %.1fs threshold", got, minCleanSpeech)
	}
}

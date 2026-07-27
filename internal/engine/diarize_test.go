package engine

import "testing"

// These are pure — no sherpa models needed, unlike the tests in engine_test.go.

func TestCosine(t *testing.T) {
	approx := func(got, want float32) bool { d := got - want; return d < 0.001 && d > -0.001 }
	if c := Cosine([]float32{1, 0}, []float32{1, 0}); !approx(c, 1) {
		t.Errorf("identical = %f, want 1", c)
	}
	if c := Cosine([]float32{1, 0}, []float32{0, 1}); !approx(c, 0) {
		t.Errorf("orthogonal = %f, want 0", c)
	}
	if c := Cosine([]float32{1, 0}, []float32{-1, 0}); !approx(c, -1) {
		t.Errorf("opposite = %f, want -1", c)
	}
	if c := Cosine([]float32{1}, []float32{1, 2}); c != 0 {
		t.Errorf("mismatched length = %f, want 0", c)
	}
	if c := Cosine(nil, nil); c != 0 {
		t.Errorf("empty = %f, want 0", c)
	}
}

func TestMergeGroupsDisabledOrNoop(t *testing.T) {
	v := []SpeakerVoice{
		{Local: 0, Embedding: []float32{1, 0}},
		{Local: 1, Embedding: []float32{0.99, 0.01}}, // would merge if enabled
	}
	if got := mergeGroups(v, 0); got != nil {
		t.Errorf("thr=0 should disable merging, got %v", got)
	}
	if got := mergeGroups(v[:1], 0.8); got != nil {
		t.Errorf("single speaker has nothing to merge, got %v", got)
	}
	distinct := []SpeakerVoice{
		{Local: 0, Embedding: []float32{1, 0}},
		{Local: 1, Embedding: []float32{0, 1}}, // cos 0
	}
	if got := mergeGroups(distinct, 0.8); got != nil {
		t.Errorf("below-threshold pair should not merge, got %v", got)
	}
}

func TestMergeGroupsCanonicalIsLowestID(t *testing.T) {
	v := []SpeakerVoice{
		{Local: 0, Embedding: []float32{0, 1}},      // distinct speaker
		{Local: 3, Embedding: []float32{1, 0}},      // over-split A
		{Local: 7, Embedding: []float32{0.99, 0.0}}, // over-split A again
	}
	remap := mergeGroups(v, 0.9)
	if remap == nil {
		t.Fatal("expected a merge")
	}
	if remap[3] != 3 || remap[7] != 3 {
		t.Errorf("3 and 7 should collapse to 3, got %d and %d", remap[3], remap[7])
	}
	if remap[0] != 0 {
		t.Errorf("unmerged speaker should keep its id, got %d", remap[0])
	}
}

// Single-link is transitive by design: A–B and B–C merge A and C even though
// A–C alone is below the threshold. Documented tradeoff, so pin the behavior.
// Was TestMergeGroupsIsTransitive, which asserted that a chain COLLAPSES —
// single linkage, deliberately, to recover an over-split speaker.
//
// A 44-minute hearing then merged into one cluster holding 2525 of its 2650
// seconds, because a single bridge cluster linked everyone. The fixture below is
// exactly that shape: v[1] sits at 0.8 from both ends while the ends are far
// apart. Transitivity is what made recovering an over-split speaker work AND
// what made this unusable, so the rule is now complete linkage — every pair in a
// group must clear the bar — and the assertion is inverted with it.
func TestMergeGroupsDoesNotChainThroughABridge(t *testing.T) {
	v := []SpeakerVoice{
		{Local: 0, Embedding: []float32{1, 0}},
		{Local: 1, Embedding: []float32{0.8, 0.6}}, // cos 0.8 to both 0 and 2
		{Local: 2, Embedding: []float32{0.28, 0.96}},
	}
	if c := Cosine(v[0].Embedding, v[2].Embedding); c >= 0.79 {
		t.Fatalf("test setup: 0–2 should be below threshold, got %f", c)
	}
	remap := mergeGroups(v, 0.79)
	if remap != nil && remap[0] == remap[2] {
		t.Errorf("bridge chained two distinct voices together: %v", remap)
	}
}

// The OOM was here: `voiceprints` handed a speaker's entire concatenated audio to
// the embedding model. Unmerged the largest cluster in a 14-minute hearing was
// ~290s and survived; `mergeGroups` then re-embeds each merged group, so a low
// merge_threshold collapsed seven clusters into one and fed ~840s into a single
// stream — 397GB virtual, 119GB resident, killed by the kernel.
func TestEmbedWindowBoundsTheInput(t *testing.T) {
	// 30s at 16kHz. A voice needs seconds, not minutes.
	if maxEmbedWindow != 30*16000 {
		t.Errorf("window is %d samples", maxEmbedWindow)
	}
	// 840s — the input that killed it — must split into bounded windows.
	const long = 840 * 16000
	n := 0
	for i := 0; i < long; i += maxEmbedWindow {
		n++
	}
	if n != 28 {
		t.Errorf("840s should be 28 windows, got %d", n)
	}
}

func TestL2NormKeepsPrintsComparable(t *testing.T) {
	// An averaged print must sit on the unit sphere, or Cosine against a
	// single-window print is measuring magnitude instead of direction.
	v := l2norm([]float32{3, 4})
	if d := Cosine(v, []float32{3, 4}); d < 0.999 {
		t.Errorf("normalising changed direction: %v", d)
	}
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n < 0.999 || n > 1.001 {
		t.Errorf("not unit length: %v", n)
	}
	// Degenerate input must not divide by zero.
	if got := l2norm([]float32{0, 0}); got[0] != 0 || got[1] != 0 {
		t.Errorf("zero vector mangled: %v", got)
	}
}

func TestCosineIsSafeOnDegenerateInput(t *testing.T) {
	if c := Cosine([]float32{1, 0}, []float32{1, 0}); c < 0.999 {
		t.Errorf("identical vectors: %v", c)
	}
	// A failed embedding must never read as a perfect match.
	for _, pair := range [][2][]float32{
		{nil, {1, 0}}, {{1, 0}, nil}, {{0, 0}, {1, 0}}, {{1, 0}, {1, 0, 0}},
	} {
		if c := Cosine(pair[0], pair[1]); c != 0 {
			t.Errorf("Cosine(%v,%v) = %v, want 0", pair[0], pair[1], c)
		}
	}
}

// The span is the decode unit, and spans are short: 121 turns on the reference
// hearing had a median of 3.1s and a maximum of 45.9s. The cap is a backstop for
// a speaker who holds the floor, not the mechanism.
func TestASRWindowsFollowTurnsAndCapPathologicalOnes(t *testing.T) {
	// A real turn decodes whole — no sub-split, no overlap padding.
	for _, d := range []float64{3.1, 14.5, 45.9, 299} {
		ws := asrWindows(100, 100+d)
		if len(ws) != 1 {
			t.Errorf("a %.1fs turn split into %d windows", d, len(ws))
		}
		if ws[0][0] != 100 || ws[0][1] != 100+d {
			t.Errorf("a whole turn was altered: %v", ws[0])
		}
	}
	// A pathological monologue is bounded.
	ws := asrWindows(0, 1200)
	if len(ws) != 4 {
		t.Fatalf("1200s at %.0fs should be 4 windows, got %d", maxASRWindow, len(ws))
	}
	for _, w := range ws {
		if d := w[1] - w[0]; d > maxASRWindow+asrOverlap+1e-9 {
			t.Errorf("window %v is %.2fs, over the bound", w, d)
		}
	}
	// Fully covered, overlapping, in order.
	if ws[0][0] != 0 || ws[len(ws)-1][1] != 1200 {
		t.Errorf("not covered: %v … %v", ws[0], ws[len(ws)-1])
	}
	for i := 1; i < len(ws); i++ {
		if ws[i][0] >= ws[i-1][1] {
			t.Errorf("gap or no overlap between %v and %v", ws[i-1], ws[i])
		}
	}
	if asrWindows(5, 5) != nil || asrWindows(9, 3) != nil {
		t.Error("empty and inverted spans must produce no windows")
	}
}

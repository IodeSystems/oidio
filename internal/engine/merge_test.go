package engine

import "testing"

func voice(local int, dims ...float32) SpeakerVoice {
	e := make([]float32, 8)
	copy(e, dims)
	return SpeakerVoice{Local: local, Embedding: e}
}

// The failure that collapsed a 44-minute hearing: A and C are plainly different
// voices, but B sits near both, and single linkage chained all three into one
// speaker. A bridge like B is common exactly where merging is tempting — it is
// usually a MIXED cluster, sitting between two real voices because it contains
// both — so complete linkage is the only safe rule here.
func TestBridgeClusterDoesNotChainTwoSpeakers(t *testing.T) {
	a := voice(0, 1, 0)
	b := voice(1, 0.75, 0.66) // close to both a and c
	c := voice(2, 0, 1)
	if Cosine(a.Embedding, b.Embedding) < 0.7 || Cosine(b.Embedding, c.Embedding) < 0.6 {
		t.Fatalf("fixture is not a bridge: a-b %.2f b-c %.2f",
			Cosine(a.Embedding, b.Embedding), Cosine(b.Embedding, c.Embedding))
	}
	if Cosine(a.Embedding, c.Embedding) > 0.3 {
		t.Fatalf("fixture ends are not distinct: a-c %.2f", Cosine(a.Embedding, c.Embedding))
	}
	remap := mergeGroups([]SpeakerVoice{a, b, c}, 0.6)
	if remap != nil && remap[0] == remap[2] {
		t.Fatalf("chained two distinct voices through a bridge: %v", remap)
	}
}

// Genuine over-splitting must still re-merge — that is what the feature is for.
func TestOverSplitSpeakerStillMerges(t *testing.T) {
	vs := []SpeakerVoice{voice(0, 1, 0.02), voice(1, 1, 0.01), voice(2, 0, 1)}
	remap := mergeGroups(vs, 0.9)
	if remap == nil {
		t.Fatal("two near-identical voiceprints should merge")
	}
	if remap[0] != remap[1] {
		t.Fatalf("the split speaker did not rejoin: %v", remap)
	}
	if remap[2] == remap[0] {
		t.Fatalf("a distinct voice was absorbed: %v", remap)
	}
}

// Merging must not depend on the order clusters happen to arrive in; single
// linkage did, because whichever link was visited first won.
func TestMergeIsOrderIndependent(t *testing.T) {
	a, b, c := voice(0, 1, 0.02), voice(1, 1, 0.01), voice(2, 0, 1)
	x := mergeGroups([]SpeakerVoice{a, b, c}, 0.9)
	y := mergeGroups([]SpeakerVoice{c, b, a}, 0.9)
	same := func(m map[int]int, p, q int) bool { return m[p] == m[q] }
	if same(x, 0, 1) != same(y, 0, 1) || same(x, 0, 2) != same(y, 0, 2) {
		t.Fatalf("order changed the outcome: %v vs %v", x, y)
	}
}

func TestMergeDisabledAtZero(t *testing.T) {
	if mergeGroups([]SpeakerVoice{voice(0, 1), voice(1, 1)}, 0) != nil {
		t.Fatal("thr 0 must disable merging entirely")
	}
}

// The threshold has to rise where a false merge is likelier, because a fixed one
// cannot serve both ends: 0.8 merged a 12-minute two-speaker hearing correctly
// and collapsed a 44-minute one into a single cluster.
func TestThresholdRisesWithClustersAndDuration(t *testing.T) {
	small := AdaptiveMergeThreshold(2, 12*60)
	many := AdaptiveMergeThreshold(16, 12*60)
	long := AdaptiveMergeThreshold(2, 44*60)
	if many <= small {
		t.Fatalf("more clusters must tighten the bar: %.3f vs %.3f", many, small)
	}
	if long <= small {
		t.Fatalf("longer audio must tighten the bar: %.3f vs %.3f", long, small)
	}
}

// The two recordings that motivated this must land where their evidence says.
// On the 12m the pair that SHOULD merge scored 0.961 and a pair that must not
// scored 0.865, so the bar has to sit between them.
func TestThresholdSeparatesTheObservedPairs(t *testing.T) {
	thr := AdaptiveMergeThreshold(10, 12*60)
	if thr <= 0.865 {
		t.Fatalf("12m bar %.3f would merge a judge with a complainant (0.865)", thr)
	}
	if thr >= 0.961 {
		t.Fatalf("12m bar %.3f would miss the one merge both signals confirmed (0.961)", thr)
	}
}

// Never merge more eagerly than the old fixed default, and never so tightly that
// the feature is off while still costing the comparison.
func TestThresholdStaysInBounds(t *testing.T) {
	for _, c := range []int{1, 2, 5, 40, 400} {
		for _, s := range []float64{60, 600, 6000, 60000} {
			got := AdaptiveMergeThreshold(c, s)
			if got < 0.80 || got > 0.95 {
				t.Fatalf("AdaptiveMergeThreshold(%d,%.0f) = %.3f out of bounds", c, s, got)
			}
		}
	}
}

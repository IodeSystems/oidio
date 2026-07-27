package server

import (
	"testing"

	"github.com/iodesystems/oidio/internal/engine"
)

// unit vectors far enough apart to be distinct voices, near enough to model a
// fragment that resembles a known speaker without clearing the full bar.
func vec(a, b float32) []float32 { return []float32{a, b, 0} }

var (
	alice    = vec(1, 0)
	bob      = vec(0, 1)
	aliceish = vec(0.92, 0.39) // ~0.92 cosine to alice: a smeared interjection
)

func known2() []knownSpeaker {
	return []knownSpeaker{{UUID: "ALICE", Embedding: alice}, {UUID: "BOB", Embedding: bob}}
}

// The rescue: a 0.4s interjection cannot found a cluster, but it only has to
// RESEMBLE a voice we already hold. Without it, this mints a phantom speaker —
// which is how a four-person hearing reports eight.
func TestFragmentIsRescuedOntoAKnownSpeaker(t *testing.T) {
	det := []engine.SpeakerVoice{
		{Local: 0, Embedding: alice},
		{Local: 1, Embedding: aliceish},
	}
	spoken := map[int]float64{0: 600, 1: 0.4}

	uuidOf, speakers := resolveSpeakersFrag(det, known2(), 0.95,
		func() string { return "MINTED" }, spoken, 3.0, 0.5, false)

	if uuidOf[1] != "ALICE" {
		t.Errorf("fragment = %q, want ALICE (rescued)", uuidOf[1])
	}
	if uuidOf[0] != "ALICE" {
		t.Errorf("main cluster = %q, want ALICE", uuidOf[0])
	}
	// The rescued fragment must not come back as a speaker to persist.
	for _, s := range speakers {
		if s.UUID == "MINTED" {
			t.Error("rescue still minted a speaker")
		}
	}
	if len(speakers) != 1 {
		t.Errorf("speakers = %d, want 1 (no duplicate ALICE)", len(speakers))
	}
}

// A long cluster is a person even if it matches nobody known — discovery still
// works, the gate applies only to fragments.
func TestLongClusterStillMintsWithoutAMatch(t *testing.T) {
	det := []engine.SpeakerVoice{{Local: 0, Embedding: vec(0, 0)}}
	uuidOf, _ := resolveSpeakersFrag(det, known2(), 0.9,
		func() string { return "MINTED" }, map[int]float64{0: 600}, 3.0, 0.5, false)
	if uuidOf[0] != "MINTED" {
		t.Errorf("got %q, want MINTED", uuidOf[0])
	}
}

// With no known voiceprints there is nothing to rescue onto. Opting in, the
// honest answer is no speaker — an invented attribution is worse than none.
func TestUnattributedFragmentGetsNoSpeaker(t *testing.T) {
	det := []engine.SpeakerVoice{{Local: 0, Embedding: aliceish}}
	spoken := map[int]float64{0: 0.4}

	uuidOf, speakers := resolveSpeakersFrag(det, nil, 0.9,
		func() string { return "MINTED" }, spoken, 3.0, 0.5, true)
	if uuidOf[0] != "" {
		t.Errorf("got %q, want \"\" (unattributed)", uuidOf[0])
	}
	if len(speakers) != 0 {
		t.Errorf("speakers = %d, want 0 — an unattributed fragment is not a speaker", len(speakers))
	}

	// Off by default: the same input still mints, so existing callers see no change.
	uuidOf2, _ := resolveSpeakersFrag(det, nil, 0.9,
		func() string { return "MINTED" }, spoken, 3.0, 0.5, false)
	if uuidOf2[0] != "MINTED" {
		t.Errorf("default path changed: got %q, want MINTED", uuidOf2[0])
	}
}

// Crosstalk is what needs human review, so it must be visible in the output.
func TestMarkOverlapsFlagsOnlyCrosstalk(t *testing.T) {
	segs := []segment{
		{ID: 0, Start: 0, End: 10, Speaker: "A"},  // B interjects inside this
		{ID: 1, Start: 3, End: 3.4, Speaker: "B"}, // the interjection
		{ID: 2, Start: 10, End: 12, Speaker: "A"}, // adjacent, not overlapping
		{ID: 3, Start: 12, End: 14, Speaker: "A"}, // same speaker, contiguous
	}
	markOverlaps(segs)
	if !segs[0].Overlap || !segs[1].Overlap {
		t.Errorf("crosstalk not flagged: %v %v", segs[0].Overlap, segs[1].Overlap)
	}
	if segs[2].Overlap || segs[3].Overlap {
		t.Error("same-speaker adjacency flagged as overlap")
	}
}

// A turn longer than Whisper's receptive field must be SPLIT, not handed over
// whole: audio past 30s is silently ignored, which is how a 12-minute recording
// came back as 75 words under a segment claiming to span the whole file.
func TestSpanOfClampsAndCuts(t *testing.T) {
	buf := make([]float32, 10*diarSampleRate) // 10s
	if got := len(spanOf(buf, 1, 3)); got != 2*diarSampleRate {
		t.Errorf("2s span = %d samples, want %d", got, 2*diarSampleRate)
	}
	if got := len(spanOf(buf, 8, 99)); got != 2*diarSampleRate {
		t.Errorf("clamp past end = %d, want %d", got, 2*diarSampleRate)
	}
	if spanOf(buf, -5, 0) != nil {
		t.Error("empty span should be nil")
	}
	if spanOf(buf, 5, 5) != nil {
		t.Error("zero-width span should be nil")
	}
}

// The chunk walk must cover a long turn end to end, with overlap, and stop.
func TestWhisperChunkWalkCoversTheTurn(t *testing.T) {
	var spans [][2]float64
	start, end := 0.0, 100.0
	for tt := start; tt < end; tt += whisperWindow - whisperOverlap {
		hi := tt + whisperWindow
		if hi > end {
			hi = end
		}
		spans = append(spans, [2]float64{tt, hi})
		if hi >= end {
			break
		}
	}
	if len(spans) < 4 {
		t.Fatalf("100s turn produced %d chunks, want >=4", len(spans))
	}
	if spans[len(spans)-1][1] != end {
		t.Errorf("walk ended at %.1f, want %.1f — tail audio would be dropped", spans[len(spans)-1][1], end)
	}
	for _, sp := range spans {
		if sp[1]-sp[0] > whisperWindow+1e-9 {
			t.Errorf("chunk %.1f-%.1f exceeds the %.0fs window", sp[0], sp[1], whisperWindow)
		}
	}
	for i := 1; i < len(spans); i++ {
		if spans[i][0] >= spans[i-1][1] {
			t.Errorf("no overlap between chunk %d and %d", i-1, i)
		}
	}
}

// The whole-file transcript is rebuilt from segments after a blend, so `text`
// and `segments` cannot disagree about what was said.
func TestJoinSegmentsRebuildsTheTranscript(t *testing.T) {
	got := joinSegments([]segment{
		{Text: "Good morning."}, {Text: "  "}, {Text: "Please raise your right hand."}, {Text: ""},
	})
	if got != "Good morning. Please raise your right hand." {
		t.Errorf("got %q", got)
	}
}
